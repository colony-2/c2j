package recipejob_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Exercise the public c2j services through both wrappers and both transports.
func TestGoalDiscovery(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
			var rt jobdb.WorkflowRuntime = db
			if backend == "http" {
				server := httptest.NewServer(remote.NewServer(db))
				t.Cleanup(server.Close)
				rt, err = remote.New(server.URL, server.Client())
				require.NoError(t, err)
			}
			wrapped := executionruntime.New(rt, execution.Allocation{})
			sdk, err := wf.NewEngineBuilder().WithRuntime(wrapped).BuildEngine()
			require.NoError(t, err)
			engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: struct {
				jobdb.WorkflowRuntime
				jobdb.JobSchemaRegistry
			}{wrapped, rt.(jobdb.JobSchemaRegistry)}}
			submit := func(id, repo string, deps ...jobdb.Dependency) jobdb.JobHandle {
				t.Helper()
				metadata, err := json.Marshal(map[string]string{"repo": repo, "cell_name": "root"})
				require.NoError(t, err)
				h, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "t", JobID: id, JobType: "candidate", Data: jobdb.NewTaskDataOrPanic(map[string]any{}), Metadata: metadata, Dependencies: deps})
				require.NoError(t, err)
				require.NotEmpty(t, h.GoalID)
				return h
			}
			a, b, exact := submit("a", "one"), submit("b", "two"), submit("exact", "two")
			key := jobdb.GoalKey{TenantId: "t", GoalID: a.GoalID}
			before, err := engine.GetGoal(ctx, key)
			require.NoError(t, err)
			_, err = engine.AddGoalCandidate(ctx, jobdb.AddGoalCandidateRequest{GoalKey: key, JobID: b.JobKey.JobId})
			require.NoError(t, err)
			// Hiding the original attempt must not remove it from goal-wide counts.
			_, err = rt.(jobdb.JobDeletionRuntime).DeleteJobs(ctx, jobdb.DeleteJobsRequest{TenantID: "t", JobIDs: []string{a.JobKey.JobId}})
			require.NoError(t, err)
			filtered, err := recipejob.ListGoals(ctx, engine, jobdb.ListGoalsRequest{TenantId: "t", Candidate: &jobdb.GoalCandidateFilter{MetadataEquals: []jobdb.MetadataPredicate{{Path: []string{"repo"}, Values: []any{"one"}}}}})
			require.NoError(t, err)
			require.Len(t, filtered.Goals, 1)
			g := filtered.Goals[0]
			require.Equal(t, key, g.GoalKey)
			require.Equal(t, before.Revision, g.Revision)
			require.EqualValues(t, 2, g.CandidateCount)
			require.EqualValues(t, 1, g.NonTerminalCandidateCount)
			require.EqualValues(t, 1, g.TerminalCandidateCount)
			page, err := recipejob.ListGoals(ctx, engine, jobdb.ListGoalsRequest{TenantId: "t", PageSize: 1})
			require.NoError(t, err)
			require.Len(t, page.Goals, 1)
			require.NotEmpty(t, page.NextPageToken)
			next, err := recipejob.ListGoals(ctx, engine, jobdb.ListGoalsRequest{TenantId: "t", PageSize: 2, PageToken: page.NextPageToken})
			require.NoError(t, err)
			require.Len(t, next.Goals, 2)
			require.Empty(t, next.NextPageToken)
			_, err = recipejob.ListGoals(ctx, engine, jobdb.ListGoalsRequest{TenantId: "other", PageToken: page.NextPageToken})
			require.Error(t, err)
			_, err = recipejob.ListGoals(ctx, engine, jobdb.ListGoalsRequest{TenantId: "t", States: []jobdb.GoalState{jobdb.GoalOpen}, PageToken: page.NextPageToken})
			require.Error(t, err)

			goalTarget, jobTarget := jobdb.DependencyTarget{GoalID: a.GoalID}, jobdb.DependencyTarget{JobID: exact.JobKey.JobId}
			consumer := submit("consumer", "one", jobdb.Dependency{Target: goalTarget, Condition: jobdb.JobPrereqSuccess}, jobdb.Dependency{Target: jobTarget, Condition: jobdb.JobPrereqSuccess})
			reverse := func(target jobdb.DependencyTarget) []jobdb.DependencyWaitInfo {
				t.Helper()
				page, err := recipejob.ListDependencyWaits(ctx, engine, jobdb.ListDependencyWaitsRequest{TenantId: "t", Target: &target})
				require.NoError(t, err)
				return page.Waits
			}
			waits := reverse(goalTarget)
			require.Len(t, waits, 1)
			require.Equal(t, consumer.JobKey, waits[0].JobKey)
			waitKey := jobdb.DependencyWaitKey{JobKey: consumer.JobKey, WaitID: waits[0].WaitID}
			info, err := rt.GetJob(ctx, consumer.JobKey)
			require.NoError(t, err)
			require.Equal(t, &waits[0], info.BlockingWait)
			jobs, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"t"}, GoalIDs: []string{consumer.GoalID}})
			require.NoError(t, err)
			require.Len(t, jobs.Jobs, 1)
			require.Equal(t, info.BlockingWait, joblist.JobFromSummary(jobs.Jobs[0]).BlockingWait)
			_, err = recipejob.ListDependencyWaits(ctx, engine, jobdb.ListDependencyWaitsRequest{TenantId: "t", Target: &goalTarget, States: []jobdb.DependencyState{jobdb.DependencySatisfied}})
			require.Error(t, err)
			run := func(h jobdb.JobHandle) {
				t.Helper()
				worker := wf.ExecutionWorker{JobType: "candidate", Execute: func(c wf.ExecutionContext, in wf.JobData) (wf.JobData, error) {
					require.Equal(t, h, c.Job())
					return in, nil
				}}
				runner, err := wf.GetJobForRun(ctx, wrapped, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: worker, WorkerID: "test", LeaseDuration: time.Minute})
				require.NoError(t, err)
				_, err = runner.Run(nil)
				require.NoError(t, err)
			}
			run(b)
			require.Empty(t, reverse(goalTarget))
			require.Len(t, reverse(jobTarget), 1)
			pending, err := recipejob.GetDependencyWait(ctx, engine, waitKey)
			require.NoError(t, err)
			require.Equal(t, jobdb.DependencyPending, pending.State)
			require.Equal(t, b.JobKey.JobId, pending.Dependencies[0].SatisfiedByJobID)
			run(exact)
			run(consumer)
			history, err := recipejob.ListDependencyWaits(ctx, engine, jobdb.ListDependencyWaitsRequest{TenantId: "t", ConsumerJobID: consumer.JobKey.JobId})
			require.NoError(t, err)
			require.Len(t, history.Waits, 1)
			require.Equal(t, jobdb.DependencySatisfied, history.Waits[0].State)
			require.Equal(t, b.JobKey.JobId, history.Waits[0].Dependencies[0].SatisfiedByJobID)
			require.Empty(t, reverse(jobTarget))
		})
	}
}

func TestGoalDiscoveryRejectsInvalidQueriesBeforeDispatch(t *testing.T) {
	_, err := recipejob.ListGoals(context.Background(), nil, jobdb.ListGoalsRequest{TenantId: "t", PageSize: -1})
	require.Error(t, err)
	_, err = recipejob.ListGoals(context.Background(), nil, jobdb.ListGoalsRequest{})
	require.Error(t, err)
	_, err = recipejob.ListDependencyWaits(context.Background(), nil, jobdb.ListDependencyWaitsRequest{TenantId: "t", ConsumerJobID: "c", PageSize: -1})
	require.Error(t, err)
	_, err = recipejob.GetDependencyWait(context.Background(), nil, jobdb.DependencyWaitKey{})
	require.Error(t, err)
}
