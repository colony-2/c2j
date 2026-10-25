package recipejob_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestGoalManagementLifecycle(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
			var rt jobdb.WorkflowRuntime = db
			if backend == "http" {
				s := httptest.NewServer(remote.NewServer(db))
				t.Cleanup(s.Close)
				rt, err = remote.New(s.URL, s.Client())
				require.NoError(t, err)
			}
			sdk, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
			require.NoError(t, err)
			engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt.(jobdb.JobSchemaRegistry)}
			submit := func(id string, deps ...jobdb.Dependency) jobdb.JobHandle {
				t.Helper()
				h, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "tenant", JobID: id, JobType: "candidate", Data: jobdb.NewTaskDataOrPanic(nil), Dependencies: deps})
				require.NoError(t, err)
				return h
			}
			get := func(h jobdb.JobHandle) jobdb.GoalInfo {
				t.Helper()
				g, err := recipejob.GetGoal(ctx, engine, jobdb.GoalKey{TenantId: "tenant", GoalID: h.GoalID})
				require.NoError(t, err)
				return g
			}
			attach := func(goal, job jobdb.JobHandle) {
				t.Helper()
				_, err := recipejob.AttachGoalCandidate(ctx, engine, jobdb.AddGoalCandidateRequest{GoalKey: get(goal).GoalKey, JobID: job.JobKey.JobId})
				require.NoError(t, err)
			}
			complete := func(h jobdb.JobHandle) error {
				worker := wf.ExecutionWorker{JobType: "candidate", Execute: func(_ wf.ExecutionContext, in wf.JobData) (wf.JobData, error) { return in, nil }}
				r, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: worker, WorkerID: h.JobKey.JobId, LeaseDuration: time.Minute})
				if err != nil {
					return err
				}
				_, err = r.Run(nil)
				return err
			}
			a, b := submit("a"), submit("b")
			attach(a, b)
			attach(a, b) // Retry must not duplicate membership.
			require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: a.JobKey}))
			require.Equal(t, jobdb.GoalOpen, get(a).State, "cancelling an attempt must not fail its goal")
			_, err = rt.(jobdb.JobDeletionRuntime).DeleteJobs(ctx, jobdb.DeleteJobsRequest{TenantID: "tenant", JobIDs: []string{a.JobKey.JobId}})
			require.NoError(t, err)
			var ids []string
			token := ""
			for {
				page, err := recipejob.ListGoalCandidates(ctx, engine, recipejob.GoalCandidatesRequest{TenantID: "tenant", GoalID: a.GoalID, PageSize: 1, PageToken: token})
				require.NoError(t, err)
				for _, j := range page.Jobs {
					ids = append(ids, j.JobID)
				}
				if page.NextPageToken == "" {
					break
				}
				require.NotEqual(t, token, page.NextPageToken)
				token = page.NextPageToken
			}
			require.ElementsMatch(t, []string{"a", "b"}, ids)
			consumer := submit("consumer", jobdb.Dependency{Target: jobdb.DependencyTarget{GoalID: a.GoalID}, Condition: jobdb.JobPrereqSuccess})
			failReq := jobdb.FailGoalRequest{GoalKey: get(a).GoalKey, Message: "attempts exhausted"}
			failed, err := recipejob.FailGoal(ctx, engine, failReq)
			require.NoError(t, err)
			retry, err := recipejob.FailGoal(ctx, engine, failReq)
			require.NoError(t, err)
			require.Equal(t, failed, retry)
			require.ElementsMatch(t, []string{"a"}, failed.Failure.TerminalJobIDs)
			require.ElementsMatch(t, []string{"b"}, failed.Failure.NonTerminalJobIDs)
			active, err := rt.GetJob(ctx, b.JobKey)
			require.NoError(t, err)
			require.Equal(t, jobdb.JobStatusReady, active.Status, "failing a goal must not cancel candidates")
			history, err := recipejob.ListDependencyWaits(ctx, engine, jobdb.ListDependencyWaitsRequest{TenantId: "tenant", ConsumerJobID: consumer.JobKey.JobId})
			require.NoError(t, err)
			require.Len(t, history.Waits, 1)
			require.Equal(t, jobdb.DependencyFailed, history.Waits[0].State)
			require.Equal(t, failed.Failure, history.Waits[0].Failure)
			require.NoError(t, complete(b))
			require.Equal(t, failed, get(a), "late success must not reopen an explicitly failed goal")
			require.Equal(t, jobdb.GoalSucceeded, get(b).State, "additional membership must not change primary goal")
			c := submit("c")
			_, err = recipejob.AttachGoalCandidate(ctx, engine, jobdb.AddGoalCandidateRequest{GoalKey: get(a).GoalKey, JobID: c.JobKey.JobId})
			require.ErrorIs(t, err, jobdb.ErrGoalConflict)
			_, err = recipejob.FailGoal(ctx, engine, jobdb.FailGoalRequest{GoalKey: get(b).GoalKey, Message: "too late"})
			require.ErrorIs(t, err, jobdb.ErrGoalConflict)
			// Attaching an already-completed candidate closes the open goal immediately.
			attach(c, b)
			require.Equal(t, b.JobKey.JobId, get(c).SuccessfulJobID)
			_, err = recipejob.GetGoal(ctx, engine, jobdb.GoalKey{TenantId: "other", GoalID: a.GoalID})
			require.ErrorIs(t, err, jobdb.ErrGoalNotFound)
			_, err = recipejob.AttachGoalCandidate(ctx, engine, jobdb.AddGoalCandidateRequest{GoalKey: jobdb.GoalKey{TenantId: "other", GoalID: a.GoalID}, JobID: b.JobKey.JobId})
			require.Error(t, err)
			_, err = recipejob.FailGoal(ctx, engine, jobdb.FailGoalRequest{GoalKey: jobdb.GoalKey{TenantId: "other", GoalID: a.GoalID}, Message: "wrong tenant"})
			require.Error(t, err)

			// Concurrent completions must leave one immutable winner and binding.
			x, y := submit("x"), submit("y")
			attach(x, y)
			follower := submit("follower", jobdb.Dependency{Target: jobdb.DependencyTarget{GoalID: x.GoalID}, Condition: jobdb.JobPrereqSuccess})
			var group sync.WaitGroup
			errs := make(chan error, 2)
			start := make(chan struct{})
			for _, h := range []jobdb.JobHandle{x, y} {
				group.Go(func() { <-start; errs <- complete(h) })
			}
			close(start)
			group.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			won := get(x)
			require.Equal(t, jobdb.GoalSucceeded, won.State)
			require.Contains(t, []string{x.JobKey.JobId, y.JobKey.JobId}, won.SuccessfulJobID)
			waits, err := recipejob.ListDependencyWaits(ctx, engine, jobdb.ListDependencyWaitsRequest{TenantId: "tenant", ConsumerJobID: follower.JobKey.JobId})
			require.NoError(t, err)
			require.Len(t, waits.Waits, 1)
			require.Equal(t, won.SuccessfulJobID, waits.Waits[0].Dependencies[0].SatisfiedByJobID)
			// Retrying the association cannot choose a different winner.
			attach(x, y)
			require.Equal(t, won, get(x))
		})
	}
}
