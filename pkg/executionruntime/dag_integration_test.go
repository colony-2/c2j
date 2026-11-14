package executionruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Real persisted runtime parents, terminal stores and visibility across both
// standalone and task-authenticated routes. No recipe or Cortex service needed.
func TestDAGPersistedSubmissionRelationships(t *testing.T) {
	for _, backend := range []string{"toy", "http-toy", "sqlite", "http-sqlite"} {
		t.Run(backend, func(t *testing.T) {
			fixtureContext := func(ctx context.Context) context.Context {
				return jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(ctx), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error { return nil })
			}
			ctx := fixtureContext(context.Background())
			var base jobdb.WorkflowRuntime = toy.New()
			if backend == "sqlite" || backend == "http-sqlite" {
				db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
				require.NoError(t, err)
				defer db.Close(ctx)
				base = db
			}
			if backend == "http-toy" || backend == "http-sqlite" {
				handler := remote.NewServer(base)
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					handler.ServeHTTP(w, r.WithContext(fixtureContext(r.Context())))
				}))
				defer s.Close()
				client, err := remote.New(s.URL, s.Client())
				require.NoError(t, err)
				base = client
			}
			rt := New(base, execution.Allocation{})
			for _, tenant := range []string{"tenant", "foreign"} {
				for _, cell := range []string{"c2j", "cortex"} {
					celltest.Create(t, rt, tenant, cell, "example.test/"+cell)
				}
			}
			submit := func(parent jobdb.ExecutionLease, id, tenant, cell, typ string, meta json.RawMessage) jobdb.JobHandle {
				req := jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: tenant, JobID: id, Cell: cell, CellRepo: "example.test/" + cell, JobType: typ, Metadata: meta, Data: jobdb.NewTaskDataOrPanic(nil)}}
				var h jobdb.JobHandle
				var err error
				if parent == nil {
					h, err = rt.SubmitJob(ctx, req)
				} else {
					h, err = parent.SubmitJob(ctx, req)
				}
				require.NoError(t, err)
				return h
			}
			claim := func(h jobdb.JobHandle, typ string) jobdb.ExecutionLease {
				l, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: h.JobKey, Routes: []jobdb.Route{{JobType: typ}}, WorkerID: "fixture", LeaseDuration: time.Minute})
				require.NoError(t, err)
				require.NotNil(t, l)
				return l
			}
			root := submit(nil, "root", "tenant", "cortex", "generic", nil)
			rootLease := claim(root, "generic")
			selected := submit(rootLease, "selected", "tenant", "c2j", "recipe", json.RawMessage(`{"title":"Selected job","parent_job_id":"forged"}`))
			selectedLease := claim(selected, "recipe")
			terminal := submit(selectedLease, "terminal", "tenant", "cortex", "generic", json.RawMessage(`{"parent_job_id":"copied-metadata-parent"}`))
			completed := claim(terminal, "generic")
			require.NoError(t, completed.Complete(ctx, jobdb.CompleteExecutionRequest{Status: "success", Chapter: &jobdb.Chapter{Ordinal: 1, TaskType: "generic", CreatedAt: time.Now().UTC(), Body: jobdb.JobAttemptOutcomeChapter{Outcome: jobdb.ApplicationOutputOutcome{Output: jobdb.ApplicationOutputBytes{Data: []byte(`{}`)}}}}}))
			submit(rootLease, "sibling", "tenant", "cortex", "generic", nil)
			hidden := submit(selectedLease, "hidden", "tenant", "c2j", "recipe", nil)
			deletion, ok := base.(jobdb.JobDeletionRuntime)
			require.True(t, ok)
			_, err := deletion.DeleteJobs(ctx, jobdb.DeleteJobsRequest{TenantID: "tenant", JobIDs: []string{hidden.JobKey.JobId}})
			require.NoError(t, err)
			submit(nil, "selected", "foreign", "c2j", "recipe", nil)
			submit(nil, "metadata-pretender", "tenant", "c2j", "recipe", json.RawMessage(`{"parent_job_id":"selected"}`))
			_, err = rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "same-goal", GoalID: selected.GoalID, Cell: "c2j", CellRepo: "example.test/c2j", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(nil)}})
			require.NoError(t, err)
			_, err = rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "wait-linked", Cell: "c2j", CellRepo: "example.test/c2j", JobType: "recipe", Prerequisites: []jobdb.JobPrerequisite{{JobID: selected.JobKey.JobId, Condition: jobdb.JobPrereqComplete}}, Data: jobdb.NewTaskDataOrPanic(nil)}})
			require.NoError(t, err)
			owner := rt.leases[selected.JobKey]
			_, scope, cancel, err := rt.restrictedAccess(ctx, wf.TaskContext{JobKey: selected.JobKey}, owner, permissions.JobDB{}, nil)
			require.NoError(t, err)
			defer cancel()
			s, err := childbroker.Start(scope, childbroker.Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "selected", GoalID: selected.GoalID}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
			require.NoError(t, err)
			defer s.Close()
			broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return s.Env()[k] })
			require.NoError(t, err)
			var want *jobinspect.DAG
			for _, api := range []jobinspect.API{&jobinspect.Service{Runtime: base, Tenant: "tenant"}, jobinspect.FromContext(scope), childbroker.JobClient{Broker: broker}} {
				d, e := jobinspect.ReadDAG(ctx, api, "selected", jobinspect.DefaultDAGOptions())
				require.NoError(t, e)
				require.True(t, d.Coverage.Complete)
				require.True(t, d.Root.Confirmed)
				require.Equal(t, "root", d.Root.ID)
				require.Len(t, d.Nodes, 5)
				ids := []string{}
				for _, n := range d.Nodes {
					ids = append(ids, n.ID)
				}
				require.ElementsMatch(t, []string{"root", "selected", "terminal", "sibling", "hidden"}, ids)
				require.Len(t, d.Edges, 4)
				for _, n := range d.Nodes {
					require.Equal(t, "tenant", n.Tenant)
					if n.ID == "terminal" {
						require.Equal(t, "success", n.CompletionStatus)
						require.Equal(t, jobdb.JobStoreArchived, n.Store)
					}
				}
				if want == nil {
					want = d
				} else {
					require.Equal(t, want.Nodes, d.Nodes)
					require.Equal(t, want.Edges, d.Edges)
					require.Equal(t, want.Coverage, d.Coverage)
				}
			}
			// Restarts copy metadata, while only lease submission records a runtime parent.
			request := jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{Cell: "cortex", CellRepo: "example.test/cortex", PriorJobKey: terminal.JobKey, LastStepToKeep: 0, JobID: "direct-restart"}}
			direct, e := base.SubmitRestartJob(ctx, request)
			require.NoError(t, e)
			standalone := &jobinspect.Service{Runtime: base, Tenant: "tenant"}
			directFamily, e := jobinspect.ReadDAG(ctx, standalone, direct.JobKey.JobId, jobinspect.DefaultDAGOptions())
			require.NoError(t, e)
			require.True(t, directFamily.Coverage.Complete)
			require.True(t, directFamily.Root.Confirmed)
			require.Len(t, directFamily.Nodes, 1)
			require.Empty(t, directFamily.Edges)
			saved, e := base.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, JobKeys: []jobdb.JobKey{direct.JobKey}, IncludeHidden: true})
			require.NoError(t, e)
			require.Len(t, saved.Jobs, 1)
			require.Contains(t, string(saved.Jobs[0].Metadata), "copied-metadata-parent")
			request.Job.JobID = "lease-restart"
			child, e := selectedLease.SubmitRestartJob(ctx, request)
			require.NoError(t, e)
			childFamily, e := jobinspect.ReadDAG(ctx, standalone, child.JobKey.JobId, jobinspect.DefaultDAGOptions())
			require.NoError(t, e)
			require.True(t, childFamily.Coverage.Complete)
			require.Contains(t, childFamily.Edges, jobinspect.SubmissionEdge{Type: "submission", Parent: selected.JobKey.JobId, Child: child.JobKey.JobId})
		})
	}
}

type dagDeniedRuntime struct {
	jobdb.WorkflowRuntime
	calls int
}

func (r *dagDeniedRuntime) ListJobs(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	r.calls++
	return jobdb.ListJobsResponse{}, &jobdb.CellError{Code: "permission_denied", Status: 403}
}
func TestDAGTaskBackendDenial(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "selected"}
	backend := &dagDeniedRuntime{}
	rt := New(backend, execution.Allocation{})
	owner := &lease{ExecutionLease: inspectionExpiryLease{expiry: time.Now().Add(time.Hour)}, leaseState: &leaseState{}}
	rt.leases[key] = owner
	_, scope, cancel, err := rt.restrictedAccess(context.Background(), wf.TaskContext{JobKey: key}, owner, permissions.JobDB{}, nil)
	require.NoError(t, err)
	defer cancel()
	server, err := childbroker.Start(scope, childbroker.Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "selected", GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer server.Close()
	broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, err)
	for _, api := range []jobinspect.API{jobinspect.FromContext(scope), childbroker.JobClient{Broker: broker}} {
		for _, op := range []string{"family-node", "family-children"} {
			_, err := api.Inspect(context.Background(), jobinspect.Request{Operation: op, JobID: "selected", Family: &jobinspect.FamilyOptions{Version: 1}, Limit: 100})
			require.Error(t, err)
			require.Equal(t, "permission_denied", jobinspect.FamilyErrorReason(err))
			require.NotContains(t, err.Error(), "upgrade")
		}
	}
	require.Equal(t, 4, backend.calls)
}
