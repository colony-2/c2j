package story_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/hookpolicy"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type submissionSetupTask struct {
	name  string
	calls atomic.Int32
	run   func(jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error)
}

func (w *submissionSetupTask) Name() string { return w.name }
func (w *submissionSetupTask) Run(ctx jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	w.calls.Add(1)
	return w.run(ctx, data)
}

func TestSubmissionRestartRerunsSetup(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		for _, failSource := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/source_failure=%t", backend, failSource), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 30*time.Second)
				defer cancel()
				var rt jobdb.WorkflowRuntime
				if backend == "toy" {
					rt = toy.New()
				} else {
					rt = storyTestRuntime(t, ctx, backend)
				}
				celltest.Create(t, rt, "submission", "test", "example.test/test")
				cellWorker := compiler.NewCellContextTaskWorker()
				cell := &submissionSetupTask{name: cellWorker.Name(), run: cellWorker.Run}
				version := 1
				source := &submissionSetupTask{name: compiler.RootSourceResolutionTaskType, run: func(jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error) {
					if failSource && version == 1 {
						return nil, fmt.Errorf("source temporarily unavailable")
					}
					return jobdb.NewTaskData(compiler.ResolvedRecipeSource{
						RecipeSourceResolution: compiler.RecipeSourceResolution{SourceKind: compiler.RecipeSourceKindServerRef, SubmittedSelector: "source-ref", ResolvedSelector: fmt.Sprintf("source-ref@v%d", version)},
						RecipeYAML: fmt.Sprintf(`id: resolved
source_hooks: from_current
input_schema:
 value: {type: string}
 api_key: {type: string}
inputs:
 value: '${{ inputs.value }}'
state:
 initial: done
 states:
  done:
   sequence: []
  unused_hook:
   include: cell://.c2/hooks/verify.yaml
outputs:
 value: '${{ inputs.value }}'
 version: %d
`, version),
					})
				}}
				policy := &submissionSetupTask{name: "recipe_hook_policy", run: func(jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error) {
					return jobdb.NewTaskData(hookpolicy.Snapshot{Revision: fmt.Sprintf("policy-v%d", version), Repository: "example.test/test"})
				}}
				worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{RootSourceResolver: compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{})})
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("submission").PlusWorkers(worker, cell, source, policy).BuildEngine()
				require.NoError(t, err)
				svc, err := story.New(story.ServiceConfig{Engine: engine, SchemaRegistry: rt.(jobdb.JobSchemaRegistry)})
				require.NoError(t, err)
				handle, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "submission", JobType: "recipe", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(workflowctl.StartJob{
					TenantId: "submission", RecipeName: "source-ref", Cell: "test", Inputs: map[string]any{"value": "submitted", "api_key": "sensitive fixture"},
				})})
				require.NoError(t, err)
				unstarted, err := svc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{ProjectID: "submission", JobID: handle.JobKey.JobId})
				require.NoError(t, err)
				require.Len(t, unstarted.RestartScopes, 1, "the engine records its header at submission, before the worker runs")
				require.Equal(t, starter.SubmissionRestartScopeID, unstarted.RestartScopes[0].ScopeID)
				run := func(key jobdb.JobKey, want jobworkflow.JobRunStatus) {
					t.Helper()
					runner, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: []jobworkflow.TaskWorker{cell, source, policy}, WorkerID: "test", LeaseDuration: time.Minute})
					require.NoError(t, err)
					out, err := runner.Run(nil)
					require.NoError(t, err)
					if out.Status != want {
						info, _ := rt.GetJob(ctx, key)
						t.Logf("job after run: route=%+v wait=%+v", info.NextRoute, info.ExecutionState.TaskWait)
					}
					require.Equal(t, want, out.Status, "%v", out.JobError)
				}
				want := jobworkflow.JobRunCompleted
				if failSource {
					want = jobworkflow.JobRunFailed
				}
				run(handle.JobKey, want)
				before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
				require.NoError(t, err)
				st, err := svc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{ProjectID: "submission", JobID: handle.JobKey.JobId})
				require.NoError(t, err)
				require.Equal(t, starter.SubmissionRestartScopeID, st.RestartScopes[0].ScopeID)
				if failSource {
					require.Len(t, st.RestartScopes, 1, "submission restart works even without a recipe scope")
				}
				snapshot, err := svc.GetRestartContext(ctx, story.GetRestartContextRequest{ProjectID: "submission", JobID: handle.JobKey.JobId, ScopeID: st.RestartScopes[0].ScopeID})
				require.NoError(t, err)
				require.Equal(t, map[string]any{"value": "submitted", "api_key": "[REDACTED]"}, snapshot.Visible["inputs"])
				require.Contains(t, snapshot.RedactedPaths, "/visible/inputs/api_key")
				require.EqualValues(t, 1, cell.calls.Load())
				require.EqualValues(t, 1, source.calls.Load(), "inspection never resolves the source")
				version = 2
				req := story.RestartRecipeJobRequest{ProjectID: "submission", JobID: handle.JobKey.JobId, ScopeID: st.RestartScopes[0].ScopeID, RequestID: uuid.NewString()}
				if !failSource {
					req.Goal.Mode = "new"
				}
				restarted, err := svc.RestartRecipeJob(ctx, req)
				require.NoError(t, err)
				if failSource {
					require.Equal(t, handle.GoalID, restarted.GoalID, "repair continues the original goal")
				}
				key := jobdb.JobKey{TenantId: restarted.TenantID, JobId: restarted.JobID}
				run(key, jobworkflow.JobRunCompleted)
				require.EqualValues(t, 2, cell.calls.Load())
				require.EqualValues(t, 2, source.calls.Load())
				policyCalls := int32(2)
				if failSource {
					policyCalls = 1
				}
				require.Equal(t, policyCalls, policy.calls.Load(), "hook policy is captured afresh")
				version = 3 // Replay must still show the recorded v2 result.
				fresh, err := svc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{ProjectID: key.TenantId, JobID: key.JobId})
				require.NoError(t, err)
				require.Equal(t, "source-ref@v2", fresh.Recipe.Source.ResolvedSelector)
				require.Equal(t, "submitted", fresh.Root.Output.(map[string]any)["value"])
				require.EqualValues(t, 2, fresh.Root.Output.(map[string]any)["version"])
				require.EqualValues(t, 2, source.calls.Load(), "replay does not resolve the new source")
				require.Equal(t, policyCalls, policy.calls.Load(), "replay does not refresh policy")
				after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
				require.NoError(t, err)
				require.Equal(t, before, after)
			})
		}
	}
}
