package jobinspect_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobinspect"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type eventReadCounter struct {
	jobdb.WorkflowRuntime
	calls int
}

func (r *eventReadCounter) ListLeaseEvents(ctx context.Context, req jobdb.ListLeaseEventsRequest) (jobdb.ListLeaseEventsResponse, error) {
	r.calls++
	return jobdb.ListLeaseEvents(ctx, r.WorkflowRuntime, req)
}

type fixtureTask struct {
	outputs []jobdb.TaskData
	calls   int
	fail    int
	retry   bool
	before  func(wf.TaskContext)
}

func (*fixtureTask) Name() string { return "inspection:run" }
func (w *fixtureTask) Run(tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	if w.before != nil {
		w.before(tc)
	}
	n := w.calls
	w.calls++
	if n == w.fail {
		if w.retry {
			return w.outputs[n], jobdb.NewSystemError(jobdb.SystemErrorPayload{Message: "temporary failure", Code: "RETRY", Retryable: true})
		}
		return w.outputs[n], fmt.Errorf("verification failed")
	}
	return w.outputs[n], nil
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	b, e := c.CombinedOutput()
	require.NoError(t, e, string(b))
	return strings.TrimSpace(string(b))
}

func TestFailedJobInspectionAndCumulativeExport(t *testing.T) {
	for _, backend := range []string{"toy", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			var rt jobdb.WorkflowRuntime = toy.New()
			if backend == "remote" {
				server := httptest.NewServer(celltest.Handler(remote.NewServer(rt)))
				defer server.Close()
				client, e := remote.New(server.URL, server.Client())
				require.NoError(t, e)
				rt = client
			}
			celltest.Create(t, rt, "tenant", "test", "example.test/test")
			origin := t.TempDir()
			git(t, origin, "init")
			require.NoError(t, os.WriteFile(filepath.Join(origin, "base.txt"), []byte("base\n"), 0644))
			git(t, origin, "add", ".")
			git(t, origin, "commit", "-m", "base")
			base := git(t, origin, "rev-parse", "HEAD")
			work := filepath.Join(t.TempDir(), "work")
			git(t, t.TempDir(), "clone", origin, work)
			var outputs []jobdb.TaskData
			var heads []string
			for n := 0; n < 3; n++ {
				require.NoError(t, os.WriteFile(filepath.Join(work, fmt.Sprintf("change%d.bin", n)), []byte{byte(n), 0, 255, '\n'}, 0755))
				if n == 1 {
					require.NoError(t, os.Remove(filepath.Join(work, "base.txt")))
				}
				git(t, work, "add", "-A")
				git(t, work, "commit", "-m", fmt.Sprintf("change%d", n))
				head := git(t, work, "rev-parse", "HEAD")
				heads = append(heads, head)
				bundle := filepath.Join(t.TempDir(), "snapshot.bundle")
				git(t, work, "bundle", "create", bundle, "HEAD", "^"+base)
				data, e := os.ReadFile(bundle)
				require.NoError(t, e)
				state := coretask.WorkspaceState{Repository: origin, Cell: "test", ScopeID: "root", BaseCommit: base, HeadCommit: head}
				env, e := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, map[string]any{"output": json.RawMessage(`{"zero":0,"false":false,"null":null,"empty":"","number":9007199254740993,"a/b":{"~x":[42]},"summary":"useful"}`), "workspace_after": state, "workspace_scope_id": "root", "git": contextual.GitCommitContext{ParentHash: base, PersistHash: head}})
				require.NoError(t, e)
				raw, e := json.Marshal(env)
				require.NoError(t, e)
				outputs = append(outputs, &jobdb.SimpleTaskData{Data: raw, Artifacts: []jobdb.Artifact{jobdb.NewArtifactFromBytes(gitstate.ThinPackArtifactName, data), jobdb.NewArtifactFromBytes("check.log", []byte{byte(n), 0, '\n'})}})
			}
			// A const/no-change operation reuses the second saved snapshot.
			outputs = append(outputs[:2], append([]jobdb.TaskData{outputs[1]}, outputs[2:]...)...)
			worker := &fixtureTask{outputs: outputs, fail: 3}
			orchestrator := wf.ExecutionWorker{JobType: "inspect-fixture", Execute: func(c wf.ExecutionContext, data jobdb.JobData) (jobdb.JobData, error) {
				for i := 0; i < 4; i++ {
					input := jobdb.NewTaskDataOrPanic(map[string]any{"context": gitstate.GlobalGitTaskContext{BaseRepo: origin, BaseRef: base, ResolvedBaseHash: base, NodePath: fmt.Sprintf("step/%d", i), Workspace: &contextual.WorkspaceContext{Cell: "test", ScopeID: "root"}}})
					_, e := c.DoTask(wf.TaskCall{Site: "repeated", TaskType: worker.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`), Policy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}, func(context.Context) (jobdb.TaskData, error) { return input, nil })
					if e != nil {
						return nil, e
					}
				}
				return data, nil
			}}
			h, e := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: orchestrator.Name(), Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil), RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}})
			require.NoError(t, e)
			counter := &eventReadCounter{WorkflowRuntime: rt}
			api := &jobinspect.Service{Runtime: counter, Tenant: "tenant"}
			var liveID string
			worker.before = func(tc wf.TaskContext) {
				if worker.calls == 1 {
					r, e := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: h.JobKey.JobId})
					require.NoError(t, e)
					require.Len(t, r.Tasks, 1)
					liveID = r.Tasks[0].ID
					require.Equal(t, "available", r.Tasks[0].OutputAvailability)
				}
			}
			run, e := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: orchestrator, TaskWorkers: []wf.TaskWorker{worker}, WorkerID: "test", LeaseDuration: time.Minute})
			require.NoError(t, e)
			out, e := run.Run(nil)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunFailed, out.Status)
			// Canonical reads are opt-in, one page, and leave retained history unchanged.
			eventReq := jobdb.ListLeaseEventsRequest{TenantID: "tenant", JobIDs: []string{h.JobKey.JobId}, PageSize: 1}
			expected, e := jobdb.ListLeaseEvents(ctx, rt, eventReq)
			require.NoError(t, e)
			require.Len(t, expected.Events, 1)
			eventPage, e := api.ListJobLeaseEvents(ctx, h.JobKey.JobId, jobinspect.LeaseEventOptions{PageSize: 1})
			require.NoError(t, e)
			require.Equal(t, expected, eventPage)
			unchanged, e := jobdb.ListLeaseEvents(ctx, rt, eventReq)
			require.NoError(t, e)
			require.Equal(t, expected, unchanged)
			if expected.NextPageToken != "" {
				eventReq.PageToken = expected.NextPageToken
				older, err := jobdb.ListLeaseEvents(ctx, rt, eventReq)
				require.NoError(t, err)
				next, err := api.ListJobLeaseEvents(ctx, h.JobKey.JobId, jobinspect.LeaseEventOptions{PageSize: 1, PageToken: expected.NextPageToken})
				require.NoError(t, err)
				require.Equal(t, older, next)
			}
			_, e = api.ListJobLeaseEvents(ctx, h.JobKey.JobId, jobinspect.LeaseEventOptions{PageToken: "tampered"})
			require.ErrorIs(t, e, jobdb.ErrInvalidLeaseEventPageToken)
			if backend == "toy" {
				denied := jobdb.WithCellAuthority(ctx, jobdb.CellAuthority{Principal: "reader", TenantID: "tenant", Permissions: map[string]bool{}})
				_, e = api.ListJobLeaseEvents(denied, h.JobKey.JobId, jobinspect.LeaseEventOptions{})
				require.ErrorIs(t, e, jobdb.ErrCellForbidden)
				excluded := jobdb.WithCellAuthority(ctx, jobdb.CellAuthority{Principal: "reader", TenantID: "tenant", Permissions: map[string]bool{"jobs.list": true}, Cells: map[string]bool{}})
				invisible, err := api.ListJobLeaseEvents(excluded, h.JobKey.JobId, jobinspect.LeaseEventOptions{})
				require.NoError(t, err)
				require.Empty(t, invisible.Events)
				otherTenant := &jobinspect.Service{Runtime: rt, Tenant: "other"}
				invisible, err = otherTenant.ListJobLeaseEvents(ctx, h.JobKey.JobId, jobinspect.LeaseEventOptions{})
				require.NoError(t, err)
				require.Empty(t, invisible.Events)
			}

			eventReads := counter.calls
			_, e = api.Inspect(ctx, jobinspect.Request{Operation: "show", JobID: h.JobKey.JobId})
			require.NoError(t, e)
			listing, e := api.Inspect(ctx, jobinspect.Request{Operation: "list"})
			require.NoError(t, e)
			require.Len(t, listing.Jobs, 1)
			require.Equal(t, eventReads, counter.calls, "ordinary show/list must not read events")
			require.Equal(t, h.JobKey.JobId, listing.Jobs[0].ID)
			q := jobinspect.Request{Operation: "log", JobID: h.JobKey.JobId, Limit: 2}
			page, e := api.Inspect(ctx, q)
			require.NoError(t, e)
			require.Len(t, page.Tasks, 2)
			require.Equal(t, liveID, page.Tasks[0].ID)
			firstID := page.Tasks[0].ID
			secondID := page.Tasks[1].ID
			require.NotEqual(t, firstID, secondID)
			require.NotEmpty(t, page.NextCursor)
			q.Cursor = page.NextCursor
			last, e := api.Inspect(ctx, q)
			require.NoError(t, e)
			require.Len(t, last.Tasks, 2)
			require.Equal(t, "failed", last.Tasks[1].Status)
			require.Contains(t, string(last.Tasks[1].Error), "verification failed")
			for pointer, want := range map[string]string{"/zero": "0", "/false": "false", "/null": "null", "/empty": "\"\"", "/number": "9007199254740993", "/a~1b/~0x/0": "42"} {
				p := pointer
				r, e := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: q.JobID, Task: secondID, Field: &p})
				require.NoError(t, e)
				require.Equal(t, want, string(r.Value))
			}
			p := "/missing"
			_, e = api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: q.JobID, Task: secondID, Field: &p})
			require.ErrorContains(t, e, "not found")
			result, e := api.Inspect(ctx, jobinspect.Request{Operation: "result", JobID: q.JobID})
			require.NoError(t, e)
			require.Equal(t, "not_produced", result.Result.Availability)
			dir := filepath.Join(t.TempDir(), "recovered")
			snap, e := jobinspect.Export(ctx, api, jobinspect.Request{JobID: q.JobID, Task: secondID}, dir)
			require.NoError(t, e)
			require.Len(t, snap.Workspaces, 1)
			require.Len(t, snap.Artifacts, 2)
			w := snap.Workspaces[0]
			require.Equal(t, heads[1], w.HeadCommit)
			require.Equal(t, base, w.BaseCommit)
			require.Equal(t, []string{base}, w.Prerequisites)
			checkout := filepath.Join(dir, w.Directory)
			require.Equal(t, heads[1], git(t, checkout, "rev-parse", "HEAD"))
			require.Equal(t, base, git(t, checkout, "rev-parse", "refs/c2j/base"))
			require.NoFileExists(t, filepath.Join(checkout, "base.txt"))
			require.NoFileExists(t, filepath.Join(checkout, "change2.bin"))
			b, e := os.ReadFile(filepath.Join(checkout, "change1.bin"))
			require.NoError(t, e)
			require.Equal(t, []byte{1, 0, 255, '\n'}, b)
			info, e := os.Stat(filepath.Join(checkout, "change1.bin"))
			require.NoError(t, e)
			require.NotZero(t, info.Mode()&0111)
			for i, a := range snap.Artifacts {
				b, e := os.ReadFile(filepath.Join(dir, a.Path))
				require.NoError(t, e)
				require.Equal(t, []byte{byte(i), 0, '\n'}, b)
			}
			_, e = jobinspect.Export(ctx, api, jobinspect.Request{JobID: q.JobID, Task: secondID}, dir)
			require.ErrorContains(t, e, "must not exist")
			latest, e := api.Inspect(ctx, jobinspect.Request{Operation: "snapshot", JobID: q.JobID})
			require.NoError(t, e)
			require.Equal(t, heads[1], latest.Snapshot.Workspaces[0].HeadCommit)
			failed, e := api.Inspect(ctx, jobinspect.Request{Operation: "snapshot", JobID: q.JobID, Task: last.Tasks[1].ID})
			require.NoError(t, e)
			require.Equal(t, heads[2], failed.Snapshot.Workspaces[0].HeadCommit)
			require.Equal(t, "failed_attempt", failed.Snapshot.Workspaces[0].State)
			reader, e := api.Download(ctx, jobinspect.Request{JobID: q.JobID, Task: secondID, Name: "check.log"})
			require.NoError(t, e)
			b, e = io.ReadAll(reader)
			require.NoError(t, e)
			reader.Close()
			require.Equal(t, []byte{1, 0, '\n'}, b)
			_, e = api.Download(ctx, jobinspect.Request{JobID: q.JobID, Reference: &jobdb.ArtifactKey{JobId: "unrelated", TaskOrdinal: 1, Name: "secret", SizeBytes: 5}})
			require.ErrorContains(t, e, "not found")
			missing := &jobinspect.Service{Runtime: missingArtifacts{rt}, Tenant: "tenant"}
			unavailable, e := missing.Inspect(ctx, jobinspect.Request{Operation: "snapshot", JobID: q.JobID, Task: secondID})
			require.NoError(t, e)
			require.Equal(t, "unavailable", unavailable.Snapshot.Workspaces[0].Availability)
			_, e = missing.Download(ctx, jobinspect.Request{JobID: q.JobID, Task: secondID, Name: "check.log"})
			require.ErrorContains(t, e, "expired")
		})
	}
}

type missingArtifacts struct{ jobdb.WorkflowRuntime }

func (missingArtifacts) OpenArtifact(context.Context, jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	return nil, fmt.Errorf("artifact expired")
}

func TestInspectionRetriesAndBoundedFields(t *testing.T) {
	ctx := celltest.Context(context.Background())
	rt := toy.New()
	celltest.Create(t, rt, "tenant", "test", "example.test/test")
	large := map[string]any{}
	for i := 0; i < 80; i++ {
		large[fmt.Sprintf("field%02d", i)] = strings.Repeat("large", 500)
	}
	worker := &fixtureTask{outputs: []jobdb.TaskData{jobdb.NewTaskDataOrPanic(map[string]any{"attempt": 1}), jobdb.NewTaskDataOrPanic(large)}, fail: 0, retry: true}
	orchestrator := wf.ExecutionWorker{JobType: "retry-inspection", Execute: func(c wf.ExecutionContext, data jobdb.JobData) (jobdb.JobData, error) {
		return c.DoTask(wf.TaskCall{Site: "retry", TaskType: worker.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`), Policy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 2, InitialInterval: jobdb.Duration(time.Millisecond)}}}, func(context.Context) (jobdb.TaskData, error) { return data, nil })
	}}
	h, e := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: orchestrator.Name(), Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, e)
	run, e := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: orchestrator, TaskWorkers: []wf.TaskWorker{worker}, WorkerID: "retry", LeaseDuration: time.Minute})
	require.NoError(t, e)
	out, e := run.Run(nil)
	require.NoError(t, e)
	require.Equal(t, wf.JobRunCompleted, out.Status, "calls=%d, error=%v", worker.calls, out.JobError)
	api := &jobinspect.Service{Runtime: rt, Tenant: "tenant"}
	r, e := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: h.JobKey.JobId})
	require.NoError(t, e)
	require.Len(t, r.Tasks, 1)
	task := r.Tasks[0]
	require.Equal(t, []int{1, 2}, task.Attempts)
	require.True(t, task.InventoryTruncated)
	require.Len(t, task.Fields, 25)
	p := "/attempt"
	first, e := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: h.JobKey.JobId, Task: task.ID, Attempt: 1, Field: &p})
	require.NoError(t, e)
	require.Equal(t, "1", string(first.Value))
	q := jobinspect.Request{Operation: "fields", JobID: h.JobKey.JobId, Task: task.ID, Attempt: 2, Limit: 15}
	page, e := api.Inspect(ctx, q)
	require.NoError(t, e)
	require.Len(t, page.Fields, 15)
	require.True(t, page.Fields[0].Truncated)
	require.NotEmpty(t, page.NextCursor)
	q.Cursor = page.NextCursor
	q.Attempt = 1
	_, e = api.Inspect(ctx, q)
	require.ErrorContains(t, e, "cursor does not match")
	q.Attempt = 2
	page, e = api.Inspect(ctx, q)
	require.NoError(t, e)
	require.Equal(t, "/field15", page.Fields[0].Pointer)
	p = "/field79"
	value, e := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: h.JobKey.JobId, Task: task.ID, Field: &p})
	require.NoError(t, e)
	require.Equal(t, `"`+strings.Repeat("large", 500)+`"`, string(value.Value))
}

func TestPointerAndBundleFailures(t *testing.T) {
	for _, p := range []string{"/x~2", "x", "/a/01", "/a/-"} {
		_, e := jobinspect.Pointer(json.RawMessage(`{"a":[0,1]}`), p)
		require.Error(t, e)
	}
	_, _, e := jobinspect.BundleHeader(strings.NewReader("not a bundle"))
	require.Error(t, e)
	_, _, e = jobinspect.BundleHeader(strings.NewReader("# v2 git bundle\n-" + strings.Repeat("a", 40) + " base\n"))
	require.Error(t, e)
}
