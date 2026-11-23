package compiler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/grouping"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/ops"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// These fixtures deliberately dispatch only when the test asks. No batching
// assertion depends on a sleep or on a worker winning a race against submission.
type groupingFixture struct {
	ctx     context.Context
	dbPath  string
	rt      jobdb.ConsumerRuntime
	guard   jobdb.ConsumerRuntime
	eng     jobdbschema.WorkflowEngine
	workers *wf.WorkSet
	start   workflowctl.StartJob
	rec     recipe.Recipe
}

func newGroupingFixture(t *testing.T, transport, yaml string) *groupingFixture {
	t.Helper()
	ctx := celltest.Context(context.Background())
	dbPath := filepath.Join(t.TempDir(), "grouping.db")
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: dbPath, Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
	var rt jobdb.ConsumerRuntime = db
	if transport == "http" {
		h := remote.NewConsumerServer(db)
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, r.WithContext(celltest.Context(r.Context())))
		}))
		t.Cleanup(s.Close)
		rt, err = remote.New(s.URL, s.Client())
		require.NoError(t, err)
	}
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "A")
	celltest.Create(t, rt, "tenant", "test", celltest.Repo(repo))
	guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
	engine, err := wf.NewEngineBuilder().WithRuntime(guard).WithWorkerTenantId("tenant").BuildEngine()
	require.NoError(t, err)
	eng := jobdbschema.WorkflowEngine{Engine: engine, Registry: rt.(jobdb.JobSchemaRegistry)}
	require.NoError(t, jobdbschema.Register(ctx, eng.Registry, "tenant"))
	deps := ops.NewServiceDepsBuilder().WithWorkflowControl(&workerworkflow.SWFWorkflowControl{Engine: eng}).Build()
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	workers, err := NewRecipeWorkerWithOptions(deps, registry, RecipeJobWorkerOptions{WrapTaskWorker: guard.WrapTaskWorker})
	require.NoError(t, err)
	require.NoError(t, eng.RegisterWorkers(workers))
	rec, err := recipe.LoadRecipeFromString([]byte(yaml))
	require.NoError(t, err)
	return &groupingFixture{ctx: ctx, dbPath: dbPath, rt: rt, guard: guard, eng: eng, workers: workers, rec: *rec, start: workflowctl.StartJob{TenantId: "tenant", Cell: "test", RecipeName: "batch", GitRef: "main", JobContext: contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}}}
}

const groupingCountRecipe = `id: batch
version: '1'
sequence: []
outputs:
 count: '${{ context.grouping.appends.size() }}'
 ids: '${{ context.grouping.appends.map(a, a.content.id) }}'
`

func (f *groupingFixture) submit(t *testing.T, key string, id any, cap int) jobref.Ref {
	t.Helper()
	r, err := starter.StartOrAppendRecipeJob(f.ctx, f.start, grouping.Spec{Present: true, Value: map[string]any{"key": key, "content": map[string]any{"id": id}, "max_items": cap}}, f.eng, f.rec)
	require.NoError(t, err)
	return r
}

func (f *groupingFixture) runRaw(t *testing.T, key jobdb.JobKey, rt jobdb.ConsumerRuntime) (wf.JobRunOutcome, error) {
	t.Helper()
	if rt == nil {
		rt = f.guard
	}
	tasks := []wf.TaskWorker{}
	for _, w := range f.workers.TaskWorkers {
		tasks = append(tasks, w)
	}
	h, err := wf.GetJobForRun(f.ctx, rt, wf.GetJobForRunRequest{JobKey: key, JobWorker: f.workers.JobWorker, TaskWorkers: tasks, WorkerID: "receiver", LeaseDuration: time.Minute})
	require.NoError(t, err)
	return h.Run(nil)
}

func (f *groupingFixture) run(t *testing.T, key jobdb.JobKey, rt jobdb.ConsumerRuntime) wf.JobRunOutcome {
	t.Helper()
	out, err := f.runRaw(t, key, rt)
	require.NoError(t, err)
	return out
}

func (f *groupingFixture) result(t *testing.T, ref jobref.Ref) map[string]any {
	t.Helper()
	out := f.run(t, ref.Key(), nil)
	require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Output.GetDataOrPanic(), &got))
	return got
}

func TestGroupingCapsConcurrencyAndIsolation(t *testing.T) {
	for _, transport := range []string{"sqlite", "http"} {
		t.Run(transport, func(t *testing.T) {
			f := newGroupingFixture(t, transport, groupingCountRecipe)
			for _, cap := range []int{1, 50} {
				key := fmt.Sprint("cap-", cap)
				first := f.submit(t, key, 0, cap)
				for i := 1; i < cap; i++ {
					require.Equal(t, first, f.submit(t, key, i, cap))
				}
				next := f.submit(t, key, cap, cap)
				require.NotEqual(t, first.JobID, next.JobID)
				info, err := f.rt.GetJob(f.ctx, next.Key())
				require.NoError(t, err)
				require.NotEqual(t, jobdb.JobStatusReady, info.Status)
				require.EqualValues(t, cap, f.result(t, first)["count"])
				require.EqualValues(t, 1, f.result(t, next)["count"])
			}
			first := f.submit(t, "extend", 1, 1)
			require.Equal(t, first, f.submit(t, "extend", 2, 3))
			require.EqualValues(t, 2, f.result(t, first)["count"])

			// Simultaneous calls, followed by receiver assertions across every
			// accepted generation, establish no lost or duplicated IDs.
			const n = 12
			refs := make([]jobref.Ref, n)
			errs := make([]error, n)
			gate := make(chan struct{})
			var wg sync.WaitGroup
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-gate
					refs[i], errs[i] = starter.StartOrAppendRecipeJob(f.ctx, f.start, grouping.Spec{Present: true, Value: map[string]any{"key": "concurrent", "max_items": 4, "content": map[string]any{"id": i}}}, f.eng, f.rec)
				}()
			}
			close(gate)
			wg.Wait()
			seenJobs, seenIDs := map[string]bool{}, map[int]bool{}
			// Generation order is the first occurrence in the durable job list,
			// not goroutine completion order. Dispatch only a READY generation.
			for _, err := range errs {
				require.NoError(t, err)
			}
			for len(seenJobs) < 3 {
				progress := false
				for _, ref := range refs {
					if seenJobs[ref.JobID] {
						continue
					}
					info, err := f.rt.GetJob(f.ctx, ref.Key())
					require.NoError(t, err)
					if info.Status != jobdb.JobStatusReady {
						continue
					}
					got := f.result(t, ref)
					for _, id := range got["ids"].([]any) {
						n := int(id.(float64))
						require.False(t, seenIDs[n])
						seenIDs[n] = true
					}
					seenJobs[ref.JobID], progress = true, true
				}
				require.True(t, progress, "every predecessor must release its successor")
			}
			require.Len(t, seenIDs, n)

			a := f.submit(t, "literal", "a", 0)
			b := f.submit(t, " literal ", "b", 0)
			require.NotEqual(t, a.JobID, b.JobID)
			otherRepo := filepath.Join(t.TempDir(), "other-repo")
			workspaceRepo(t, otherRepo, "B")
			celltest.Create(t, f.rt, "tenant", "other", celltest.Repo(otherRepo))
			f.start.Cell = "other"
			c := f.submit(t, "literal", "c", 0)
			require.NotEqual(t, a.CellID, c.CellID)
			f.start.Cell, f.start.TenantId = "test", "foreign"
			celltest.Create(t, f.rt, "foreign", "test", celltest.Repo(f.start.JobContext.GitBase.BaseRepo))
			require.NoError(t, jobdbschema.Register(f.ctx, f.eng.Registry, "foreign"))
			d := f.submit(t, "literal", "d", 0)
			require.NotEqual(t, a.JobID, d.JobID)
			for _, ref := range []jobref.Ref{a, b, c, d} {
				require.EqualValues(t, 1, f.result(t, ref)["count"])
			}
		})
	}
}

type groupingFirstWriteConflict struct {
	jobdb.ConsumerRuntime
	writes int
}

func (r *groupingFirstWriteConflict) PutChapter(ctx context.Context, q jobdb.PutChapterRequest) error {
	r.writes++
	if r.writes == 1 {
		return jobdb.ErrConflict
	}
	return r.ConsumerRuntime.PutChapter(ctx, q)
}

func TestGroupingFirstWriteRecoveryAndRestart(t *testing.T) {
	for _, transport := range []string{"sqlite", "http"} {
		t.Run(transport, func(t *testing.T) {
			f := newGroupingFixture(t, transport, groupingCountRecipe)
			first := f.submit(t, "restart", 1, 0)
			fault := &groupingFirstWriteConflict{ConsumerRuntime: f.guard}
			require.Equal(t, wf.JobRunSuspended, f.run(t, first.Key(), fault).Status)
			require.Equal(t, first, f.submit(t, "restart", 2, 0), "rejected first write must leave input appendable")
			require.EqualValues(t, 2, f.result(t, first)["count"])
			run, err := jobdb.GetJobRun(f.ctx, f.rt, jobdb.GetJobRunRequest{JobKey: first.Key()})
			require.NoError(t, err)
			var point string
			for _, event := range run.Execution {
				if event.Record.Kind == "task" {
					point = event.Record.ID
					break
				}
			}
			require.NotEmpty(t, point)
			restarted, err := f.rt.SubmitRestartJob(f.ctx, jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: first.Key(), ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: point}, Cell: "test"}})
			require.NoError(t, err)
			restartInfo, err := f.rt.GetJob(f.ctx, restarted.JobKey)
			require.NoError(t, err)
			require.Empty(t, restartInfo.AppendKey)
			next := f.submit(t, "restart", 3, 0)
			require.NotEqual(t, first.JobID, next.JobID)
			// Restart remains pending but does not hold the original chain.
			require.EqualValues(t, 1, f.result(t, next)["count"])
			restartRef, err := jobref.FromHandle(restarted)
			require.NoError(t, err)
			require.EqualValues(t, 2, f.result(t, restartRef)["count"])
		})
	}
}

func TestGroupingSuccessorWaitsAndNoWriteLease(t *testing.T) {
	for _, transport := range []string{"sqlite", "http"} {
		t.Run(transport, func(t *testing.T) {
			f := newGroupingFixture(t, transport, groupingCountRecipe)
			lease := func(ref jobref.Ref) jobdb.ExecutionLease {
				l, err := f.rt.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: ref.Key(), WorkerID: "held", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: time.Minute})
				require.NoError(t, err)
				require.NotNil(t, l)
				return l
			}
			first := f.submit(t, "no-write", 1, 0)
			l := lease(first)
			require.NoError(t, l.Reschedule(f.ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}))
			require.ErrorIs(t, l.KeepAlive(f.ctx), jobdb.ErrExecutionLeaseLost)
			require.Equal(t, first, f.submit(t, "no-write", 2, 0))
			require.EqualValues(t, 2, f.result(t, first)["count"])

			for _, terminal := range []string{"failed_app", "cancelled"} {
				key := "held-" + terminal
				first := f.submit(t, key, 1, 0)
				l := lease(first)
				next := f.submit(t, key, 2, 0)
				require.NotEqual(t, first.JobID, next.JobID, "worker-held tail requires successor")
				assertBlocked := func() {
					info, err := f.rt.GetJob(f.ctx, next.Key())
					require.NoError(t, err)
					require.NotEqual(t, jobdb.JobStatusReady, info.Status)
				}
				assertBlocked()
				if terminal == "cancelled" {
					require.NoError(t, f.rt.CancelJob(f.ctx, jobdb.CancelJobRequest{JobKey: first.Key(), Reason: "cancel shared batch"}))
					cancelled, err := f.rt.GetJob(f.ctx, first.Key())
					require.NoError(t, err)
					require.Equal(t, jobdb.JobStatusCancelled, cancelled.Status)
				} else {
					require.NoError(t, l.Complete(f.ctx, jobdb.CompleteExecutionRequest{Status: terminal, Chapter: &jobdb.Chapter{Ordinal: 1, TaskType: "recipe", CreatedAt: time.Now().UTC(), Body: jobdb.JobAttemptOutcomeChapter{Outcome: jobdb.AppErrorOutcome{Error: jobdb.AppErrorPayload{Message: terminal}}}}}))
				}
				require.EqualValues(t, 1, f.result(t, next)["count"])
			}
			cancelled := f.submit(t, "cancelled-tail", 1, 0)
			require.NoError(t, f.rt.CancelJob(f.ctx, jobdb.CancelJobRequest{JobKey: cancelled.Key()}))
			afterCancel := f.submit(t, "cancelled-tail", 2, 0)
			require.NotEqual(t, cancelled.JobID, afterCancel.JobID)
			require.EqualValues(t, 1, f.result(t, afterCancel)["count"])
			// A successful first publication seals the input even if settlement
			// is interrupted and the same generation becomes runnable again.
			first = f.submit(t, "sealed", 1, 0)
			_, err := f.runRaw(t, first.Key(), goalCompletionInterruption{f.guard})
			require.ErrorContains(t, err, "injected interruption")
			next := f.submit(t, "sealed", 2, 0)
			require.NotEqual(t, first.JobID, next.JobID)
			require.EqualValues(t, 1, f.result(t, first)["count"])
			require.EqualValues(t, 1, f.result(t, next)["count"])
		})
	}
}

func TestGroupingCancellationRequestDoesNotReleaseSuccessor(t *testing.T) {
	for _, transport := range []string{"sqlite", "http"} {
		for _, requestFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/request-before-successor=%v", transport, requestFirst), func(t *testing.T) {
				f := newGroupingFixture(t, transport, groupingCountRecipe)
				first := f.submit(t, "cancel-request", "predecessor", 0)
				var next jobref.Ref
				if !requestFirst {
					// Also cover a successor already waiting behind a worker-held job.
					lease, err := f.rt.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: first.Key(), WorkerID: "held", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: time.Minute})
					require.NoError(t, err)
					require.NotNil(t, lease)
					next = f.submit(t, "cancel-request", "successor", 0)
				}

				// SQLite CancelJob requests and settles cancellation atomically.
				// Seed only the request marker to deterministically hold the earlier
				// persisted state; do not mock dependency evaluation or acquisition.
				db, err := sql.Open("sqlite", f.dbPath)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close()) })
				updated, err := db.ExecContext(f.ctx, `UPDATE jobdb_jobs SET cancel_requested=1
WHERE tenant_id=? AND job_id=? AND cancel_requested=0 AND archived_at_ns IS NULL AND completion_status IS NULL`, first.TenantID, first.JobID)
				require.NoError(t, err)
				n, err := updated.RowsAffected()
				require.NoError(t, err)
				require.EqualValues(t, 1, n)

				assertRequestOnly := func() {
					t.Helper()
					jobs, err := f.rt.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{first.TenantID}, JobKeys: []jobdb.JobKey{first.Key()}})
					require.NoError(t, err)
					require.Len(t, jobs.Jobs, 1)
					require.True(t, jobs.Jobs[0].CancelRequested)
					require.Nil(t, jobs.Jobs[0].ArchivedAt, "request marker is not terminal settlement")
					require.Empty(t, jobs.Jobs[0].CompletionStatus)
				}
				assertRequestOnly()
				if requestFirst {
					// No worker holds this tail: the request alone forces rollover.
					next = f.submit(t, "cancel-request", "successor", 0)
				}
				require.NotEqual(t, first.JobID, next.JobID)
				pending, err := f.rt.GetJob(f.ctx, next.Key())
				require.NoError(t, err)
				require.Equal(t, []string{first.JobID}, pending.WaitFor)
				require.Equal(t, jobdb.JobStatusPendingJobs, pending.Status)
				lease, err := f.rt.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: next.Key(), WorkerID: "successor", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: time.Minute})
				require.NoError(t, err)
				require.Nil(t, lease, "targeted acquisition must not treat a request as completion")
				leases, err := f.rt.PollWork(f.ctx, jobdb.PollWorkRequest{TenantId: first.TenantID, WorkerID: "poller", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: time.Minute})
				require.NoError(t, err)
				require.Empty(t, leases, "polling must not lease either the cancelled-request tail or its successor")
				assertRequestOnly()
				t.Log("request persisted; predecessor unarchived with no completion; successor PENDING_JOBS; targeted and polled acquisition blocked")

				// Settle through the actual runtime API, then execute the receiver.
				require.NoError(t, f.rt.CancelJob(f.ctx, jobdb.CancelJobRequest{JobKey: first.Key(), Reason: "settle held cancellation request"}))
				jobs, err := f.rt.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{first.TenantID}, JobKeys: []jobdb.JobKey{first.Key()}})
				require.NoError(t, err)
				require.Len(t, jobs.Jobs, 1)
				require.True(t, jobs.Jobs[0].CancelRequested)
				require.NotNil(t, jobs.Jobs[0].ArchivedAt)
				require.Equal(t, "cancelled", jobs.Jobs[0].CompletionStatus)
				ready, err := f.rt.GetJob(f.ctx, next.Key())
				require.NoError(t, err)
				require.Equal(t, jobdb.JobStatusReady, ready.Status)
				got := f.result(t, next)
				require.EqualValues(t, 1, got["count"])
				require.Equal(t, []any{"successor"}, got["ids"])
				t.Log("predecessor archived as cancelled; successor READY and receiver completed with only its accepted contribution")
			})
		}
	}
}

func TestGroupingUnavailableAndForeignReferences(t *testing.T) {
	withRegisteredOps(t, commandop.GetOp())
	for _, transport := range []string{"sqlite", "http"} {
		for _, foreign := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/foreign=%v", transport, foreign), func(t *testing.T) {
				f := newGroupingFixture(t, transport, `id: batch
sequence:
- id: read
  op: command_execution
  artifacts: {input.txt: '${{ context.grouping.appends[0].artifacts[0] }}'}
  inputs: {run: 'cat "${{ context.environment.op.inbox }}/input.txt"'}
outputs: {text: '${{ sequence.read.outputs.stdout }}'}
`)
				key := jobdb.ArtifactKey{JobId: "unavailable", TaskOrdinal: 0, Name: "private.txt", SizeBytes: 6}
				if foreign {
					celltest.Create(t, f.rt, "foreign", "test", celltest.Repo(f.start.JobContext.GitBase.BaseRepo))
					h, err := f.rt.SubmitJob(f.ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "foreign", Cell: "test", JobType: "source", Data: jobdb.NewTaskDataOrPanic(nil, jobdb.NewArtifactFromBytes(key.Name, []byte("secret")))}})
					require.NoError(t, err)
					key.JobId = h.JobKey.JobId
				}
				ref, err := starter.StartOrAppendRecipeJob(f.ctx, f.start, grouping.Spec{Present: true, Value: map[string]any{"key": "missing", "artifacts": []artifacts.Ref{artifacts.NewStoredRef(key)}}}, f.eng, f.rec)
				require.NoError(t, err, "admission must not preflight bytes")
				out := f.run(t, ref.Key(), nil)
				require.Equal(t, wf.JobRunFailed, out.Status)
				require.Error(t, out.JobError)
				require.Contains(t, out.JobError.Error(), `artifact binding "input.txt"`)
				require.Contains(t, out.JobError.Error(), "job not found")
				require.NotContains(t, out.JobError.Error(), "secret")
			})
		}
	}
}

func TestGroupingLeaseExpiryDoesNotReleaseSuccessor(t *testing.T) {
	for _, transport := range []string{"sqlite", "http"} {
		t.Run(transport, func(t *testing.T) {
			f := newGroupingFixture(t, transport, groupingCountRecipe)
			first := f.submit(t, "expiry", 1, 0)
			lease, err := f.rt.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: first.Key(), WorkerID: "held", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: time.Second})
			require.NoError(t, err)
			require.NotNil(t, lease)
			next := f.submit(t, "expiry", 2, 0)
			require.NotEqual(t, first.JobID, next.JobID)
			// Wait on the lease's actual deadline, then prove expiry; never infer
			// settlement or release from elapsed time alone.
			held, err := f.rt.ListLeaseEvents(f.ctx, jobdb.ListLeaseEventsRequest{TenantID: "tenant", JobIDs: []string{first.JobID}, WorkerIDs: []string{"held"}})
			require.NoError(t, err)
			require.Len(t, held.Events, 1)
			timer := time.NewTimer(time.Until(held.Events[0].LeaseExpiresAt))
			defer timer.Stop()
			<-timer.C
			require.ErrorIs(t, lease.KeepAlive(f.ctx), jobdb.ErrExecutionLeaseLost)
			info, err := f.rt.GetJob(f.ctx, next.Key())
			require.NoError(t, err)
			require.NotEqual(t, jobdb.JobStatusReady, info.Status, "expiry is not terminal completion")
			require.EqualValues(t, 1, f.result(t, first)["count"])
			require.EqualValues(t, 1, f.result(t, next)["count"])
		})
	}
}

func TestGroupingChapterBudgets(t *testing.T) {
	for _, transport := range []string{"sqlite", "http"} {
		t.Run(transport, func(t *testing.T) {
			f := newGroupingFixture(t, transport, groupingCountRecipe)
			limit := int(jobdb.DefaultMaxChapterApplicationPayloadBytes)
			// Both parts are individually below the limit; together they fail.
			f.start.Inputs = map[string]any{"padding": strings.Repeat("x", limit*3/5)}
			_, err := starter.StartOrAppendRecipeJob(f.ctx, f.start, grouping.Spec{Present: true, Value: map[string]any{"key": "oversize-first", "content": map[string]any{"id": strings.Repeat("y", limit*3/5)}}}, f.eng, f.rec)
			require.ErrorIs(t, err, jobdb.ErrChapterApplicationPayloadTooLarge)
			f.start.Inputs = nil
			first := f.submit(t, "later", 1, 0)
			_, err = starter.StartOrAppendRecipeJob(f.ctx, f.start, grouping.Spec{Present: true, Value: map[string]any{"key": "later", "content": map[string]any{"id": strings.Repeat("y", limit)}}}, f.eng, f.rec)
			require.ErrorIs(t, err, jobdb.ErrChapterApplicationPayloadTooLarge)
			require.EqualValues(t, 1, f.result(t, first)["count"], "size failure must not create another contribution")
			// Large inputs stay in context; only small derived outputs are
			// checkpointed. The assembled prefix exceeds one chapter budget.
			largeRecipe, err := recipe.LoadRecipeFromString([]byte(`id: batch
sequence: []
outputs:
 count: '${{ context.grouping.appends.size() }}'
 sizes: '${{ context.grouping.appends.map(a, a.content.id.size()) }}'
`))
			require.NoError(t, err)
			f.rec = *largeRecipe
			a := f.submit(t, "assembled", strings.Repeat("x", limit/2), 0)
			for range 2 {
				require.Equal(t, a, f.submit(t, "assembled", strings.Repeat("y", limit/2), 0))
			}
			got := f.result(t, a)
			require.EqualValues(t, 3, got["count"])
			require.Equal(t, []any{float64(limit / 2), float64(limit / 2), float64(limit / 2)}, got["sizes"])
		})
	}
}

func TestGroupingRenderedSubmissionValidation(t *testing.T) {
	withRegisteredOps(t, recipeops.GetOps()...)
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			f := newGroupingFixture(t, "sqlite", `schema_version: 2
id: batch
input_schema:
 group: {type: object, required: true}
inputs: {group: '${{ inputs.group }}'}
sequence:
- id: submit
  op: recipes.run
  inputs:
   git_ref: main
   recipes:
   - name: receiver
     cell_name: test
     grouping: '${{ inputs.group }}'
outputs: {items: '${{ sequence.submit.outputs.items }}'}
`)
			var cap any = 2
			if !valid {
				cap = "2"
			}
			f.start.Inputs = map[string]any{"group": map[string]any{"key": "rendered", "content": map[string]any{"native": []any{true, nil, 2}}, "max_items": cap}}
			parent := f.submit(t, "parent", 1, 0)
			out := f.run(t, parent.Key(), nil)
			if !valid {
				require.Equal(t, wf.JobRunFailed, out.Status)
				require.ErrorContains(t, out.JobError, "grouping.max_items")
			} else {
				require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
			}
			jobs, err := f.rt.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, AppendKeys: []string{"rendered"}})
			require.NoError(t, err)
			if !valid {
				require.Empty(t, jobs.Jobs)
				return
			}
			require.Len(t, jobs.Jobs, 1)
			chapters, err := f.rt.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: jobs.Jobs[0].JobKey})
			require.NoError(t, err)
			body := chapters[0].Body.(jobdb.JobStartChapter)
			require.JSONEq(t, `{"content":{"native":[true,null,2]},"artifacts":[]}`, string(body.Append.Data))
		})
	}
}
