package executionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/inputhistorytest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/input/historybridge"
	"github.com/colony-2/c2j/pkg/jobinputs"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type historyBlocked struct {
	started, closed chan struct{}
	once            sync.Once
	closes          atomic.Int32
}

func (b *historyBlocked) Read([]byte) (int, error) {
	close(b.started)
	<-b.closed
	return 0, errors.New("closed")
}
func (b *historyBlocked) Close() error {
	b.closes.Add(1)
	b.once.Do(func() { close(b.closed) })
	return nil
}

type historyFailedRenewal struct{ *selfLease }

var _ jobdb.RenewableExecutionLease = (*historyFailedRenewal)(nil)

func (*historyFailedRenewal) LeaseWorkerID() string { return "history-worker" }

func (*historyFailedRenewal) Renew(context.Context) (jobdb.RenewableExecutionLease, error) {
	return nil, errors.New("renewal rejected")
}

func TestInputHistoryLeaseLifetime(t *testing.T) {
	for _, mode := range []string{"scope", "request", "forget", "replace", "expiry", "renew-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
			rt := New(nil, execution.Allocation{})
			expiry := time.Now().Add(time.Hour)
			if mode == "expiry" {
				expiry = time.Now().Add(150 * time.Millisecond)
			}
			var backing jobdb.ExecutionLease = &selfLease{key: key, expiry: expiry}
			if mode == "renew-failure" {
				backing = &historyFailedRenewal{backing.(*selfLease)}
			}
			wrapped := rt.wrapLease(backing, &leaseState{})
			var owner *lease
			if renewed, ok := wrapped.(*renewableLease); ok {
				owner = renewed.lease
			} else {
				owner = wrapped.(*lease)
			}
			tc := wf.TaskContext{JobKey: key}
			scopeCtx, scopeCancel := context.WithCancel(ctx)
			defer scopeCancel()
			access := permissions.NewAccess(scopeCtx, permissions.JobDB{}, nil, nil, nil)
			request, requestCancel := context.WithCancel(ctx)
			defer requestCancel()
			bound, cancel := rt.bindHistory(request, access, tc, owner)
			defer cancel()
			source := &historyBlocked{started: make(chan struct{}), closed: make(chan struct{})}
			stream := jobinputs.VerifiedStream(bound, source, 1, nil)
			done := make(chan error, 1)
			go func() { _, e := io.ReadAll(stream); done <- e }()
			<-source.started
			switch mode {
			case "scope":
				scopeCancel()
			case "request":
				requestCancel()
			case "forget":
				owner.forget()
			case "renew-failure":
				_, e := wrapped.(jobdb.RenewableExecutionLease).Renew(ctx)
				require.ErrorContains(t, e, "renewal rejected")
			case "replace":
				rt.wrapLease(&selfLease{key: key, expiry: time.Now().Add(time.Hour)}, &leaseState{})
			}
			want := error(jobdb.ErrExecutionLeaseLost)
			if mode == "scope" || mode == "request" {
				want = context.Canceled
			}
			select {
			case e := <-done:
				require.ErrorIs(t, e, want)
			case <-time.After(2 * time.Second):
				t.Fatal("lease/scope did not interrupt blocked read")
			}
			require.ErrorIs(t, stream.Close(), want)
			require.Equal(t, int32(1), source.closes.Load())
		})
	}
}
func TestInputHistoryRenewalPreservesAuthority(t *testing.T) {
	ctx := context.Background()
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	rt := New(nil, execution.Allocation{})
	owner := rt.wrapLease(&selfLease{key: key, expiry: time.Now().Add(100 * time.Millisecond)}, &leaseState{}).(*lease)
	access := permissions.NewAccess(ctx, permissions.JobDB{}, nil, nil, nil)
	bound, cancel := rt.bindHistory(ctx, access, wf.TaskContext{JobKey: key}, owner)
	defer cancel()
	rt.wrapLease(&selfLease{key: key, expiry: time.Now().Add(time.Hour)}, owner.leaseState)
	select {
	case <-bound.Done():
		t.Fatalf("same-authority renewal canceled: %v", context.Cause(bound))
	case <-time.After(200 * time.Millisecond):
	}
	owner.forget()
	select {
	case <-bound.Done():
		require.ErrorIs(t, context.Cause(bound), jobdb.ErrExecutionLeaseLost)
	case <-time.After(time.Second):
		t.Fatal("forgotten renewal did not cancel")
	}
}
func TestInputHistoryCachedOperationsLifetime(t *testing.T) {
	ctx := celltest.Context(context.Background())
	db, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "history.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, e)
	defer db.Close(ctx)
	f, e := inputhistorytest.Build(ctx, db)
	require.NoError(t, e)
	require.NoError(t, f.Advance(ctx, 0))
	for _, mode := range []string{"scope", "lost", "replaced", "expired"} {
		t.Run(mode, func(t *testing.T) {
			rt := New(db, execution.Allocation{})
			rt.InputHistory = historybridge.Factory(db, f.Control)
			owner := rt.wrapLease(&selfLease{key: f.Job, expiry: time.Now().Add(time.Hour)}, &leaseState{}).(*lease)
			tc := wf.TaskContext{JobKey: f.Job}
			_, scope, stop, e := rt.restrictedAccess(ctx, tc, owner, permissions.JobDB{}, nil)
			require.NoError(t, e)
			defer stop()
			api, e := jobinputs.NewFromContext(scope)
			require.NoError(t, e)
			require.NotNil(t, api)
			list := jobinputs.Request{Version: 1, Operation: "list", JobID: f.Job.JobId, Limit: 1}
			out, e := api.Inspect(ctx, list)
			require.NoError(t, e)
			var p input.JobInputStoryPage
			require.NoError(t, json.Unmarshal(out.Page, &p))
			show := jobinputs.Request{Version: 1, Operation: "show", JobID: f.Job.JobId, EntryID: p.Entries[0].EntryID, Snapshot: p.SnapshotToken}
			out, e = api.Inspect(ctx, show)
			require.NoError(t, e)
			var d input.JobInputStoryEntryDetail
			require.NoError(t, json.Unmarshal(out.Entry, &d))
			artifact := show
			artifact.Operation = "artifact"
			artifact.ArtifactID = d.RequestManifest[0].ArtifactID
			continuation := list
			continuation.Cursor = p.NextPageToken
			switch mode {
			case "scope":
				stop()
			case "lost":
				owner.forget()
			case "replaced":
				rt.wrapLease(&selfLease{key: f.Job, expiry: time.Now().Add(time.Hour)}, &leaseState{})
			case "expired":
				rt.wrapLease(&selfLease{key: f.Job, expiry: time.Now().Add(-time.Second)}, owner.leaseState)
			}
			for _, q := range []jobinputs.Request{list, continuation, show, artifact} {
				var e error
				if q.Operation == "artifact" {
					_, e = api.Open(ctx, q)
				} else {
					_, e = api.Inspect(ctx, q)
				}
				require.Error(t, e)
			}
		})
	}
}

type blockedDescriptor struct {
	jobdb.ArtifactReader
	source *historyBlocked
}

func (d blockedDescriptor) Open() (io.ReadCloser, error) { return d.source, nil }
func TestInputHistoryActiveTaskCancellation(t *testing.T) {
	ctx := celltest.Context(context.Background())
	db, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "task.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, e)
	defer db.Close(ctx)
	f, e := inputhistorytest.Build(ctx, db)
	require.NoError(t, e)
	policy := &inputhistorytest.Policy{ConsumerRuntime: db}
	source := &historyBlocked{started: make(chan struct{}), closed: make(chan struct{})}
	policy.Fault(func(_ context.Context, ref jobdb.ArtifactRef, a jobdb.ArtifactReader) (jobdb.ArtifactReader, error) {
		if ref.JobKey.JobId == f.Source.JobId {
			return blockedDescriptor{a, source}, nil
		}
		return a, nil
	})
	rt := New(policy, execution.Allocation{})
	rt.InputHistory = historybridge.Factory(policy, f.Control)
	finished := make(chan error, 1)
	work := delegationTask(func(local context.Context, _ wf.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
		api, e := jobinputs.NewFromContext(local)
		if e != nil {
			return nil, e
		}
		p, e := api.Inspect(local, jobinputs.Request{Version: 1, Operation: "list", JobID: f.Job.JobId})
		if e != nil {
			return nil, e
		}
		var page input.JobInputStoryPage
		if e = json.Unmarshal(p.Page, &page); e != nil {
			return nil, e
		}
		show := jobinputs.Request{Version: 1, Operation: "show", JobID: f.Job.JobId, EntryID: page.Entries[0].EntryID, Snapshot: page.SnapshotToken}
		d, e := api.Inspect(local, show)
		if e != nil {
			return nil, e
		}
		var detail input.JobInputStoryEntryDetail
		if e = json.Unmarshal(d.Entry, &detail); e != nil {
			return nil, e
		}
		show.Operation = "artifact"
		for _, m := range detail.RequestManifest {
			if k, ok := m.Ref.StoredKey(); ok && k.JobId == f.Source.JobId {
				show.ArtifactID = m.ArtifactID
			}
		}
		stream, e := api.Open(local, show)
		if e != nil {
			return nil, e
		}
		_, e = io.ReadAll(stream)
		e = errors.Join(e, stream.Close())
		finished <- e
		return nil, e
	})
	worker := wf.ExecutionWorker{JobType: "probe-history", Execute: func(c wf.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		return c.DoTask(wf.TaskCall{Site: "probe", TaskType: work.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return jobdb.NewTaskDataOrPanic(nil), nil })
	}}
	h, e := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: inputhistorytest.Tenant, JobID: "probe", JobType: "probe-history", Cell: "history", CellRepo: "example.test/history", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, e)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runnable, e := wf.GetJobForRun(runCtx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: worker, TaskWorkers: []wf.TaskWorker{rt.WrapTaskWorker(work)}, WorkerID: "probe", LeaseDuration: time.Minute})
	require.NoError(t, e)
	done := make(chan struct{})
	go func() { defer close(done); _, _ = runnable.Run(nil) }()
	select {
	case <-source.started:
	case e := <-finished:
		t.Fatalf("task failed before read: %v", e)
	case <-time.After(5 * time.Second):
		t.Fatal("task never started source read")
	}
	cancel()
	select {
	case e := <-finished:
		require.ErrorIs(t, e, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("active task cancellation left read blocked")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not drain")
	}
	require.Equal(t, int32(1), source.closes.Load())
}
