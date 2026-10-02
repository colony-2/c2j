package compiler

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type timeoutReproSubmitter struct {
	inner  jobworkflow.Engine
	policy jobdb.RunPolicy
}

func (s *timeoutReproSubmitter) SubmitJob(ctx context.Context, job jobdb.SubmitJob) (jobdb.JobKey, error) {
	s.policy = job.RunPolicy
	return s.inner.SubmitJob(ctx, job)
}

type timeoutReproActivity struct {
	name   string
	output jobdb.TaskData
	runs   atomic.Int32
}

func (w *timeoutReproActivity) Name() string { return w.name }
func (w *timeoutReproActivity) Run(jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error) {
	w.runs.Add(1)
	return w.output, nil
}

func TestRecipeDeadlineAcrossRecovery(t *testing.T) {
	// Timely external completion is deliberately used: this does NOT test the
	// late-response race. Recovery must consume the cached input result but
	// must not start unfinished work after the original scope deadline.
	waitOp, err := coreops.NewOp().WithType("timeout_repro_input").AddStep("wait", coreops.NewNoTaskStep[map[string]any, map[string]any]()).Build()
	require.NoError(t, err)
	finishOp := newTimeoutTestOp(t, "timeout_repro_finish", 0)
	withRegisteredOps(t, waitOp.(coreops.RegisterableOp), finishOp)
	// Leave room for schema validation, HTTP, and race instrumentation.
	// Expiry cases wait past this explicit budget; the success control must
	// not depend on finishing replay within a 100ms window.
	const budget = 3 * time.Second
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		for _, scope := range []string{"preloaded-root", "runtime-resolved-root", "preloaded-nested", "completed-nested", "timely-runtime-root"} {
			t.Run(backend+"/"+scope, func(t *testing.T) {
				source := fmt.Sprintf(`id: timeout_repro_recipe
timeout: %s
sequence:
  - id: prompt
    op: timeout_repro_input
  - id: finish
    op: timeout_repro_finish
`, budget)
				if scope == "preloaded-nested" {
					source = fmt.Sprintf(`id: timeout_repro_recipe
sequence:
  - id: bounded
    timeout: %s
    sequence:
      - id: prompt
        op: timeout_repro_input
      - id: finish
        op: timeout_repro_finish
`, budget)
				}
				if scope == "completed-nested" {
					source = fmt.Sprintf(`id: timeout_repro_recipe
sequence:
  - id: bounded
    timeout: %s
    sequence:
      - id: prompt
        op: timeout_repro_input
  - id: finish
    op: timeout_repro_finish
`, budget)
				}
				rec, err := recipe.LoadRecipeFromString([]byte(source))
				require.NoError(t, err)
				rt := newTimeoutReproRuntime(t, backend)
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, err)
				submitter := &timeoutReproSubmitter{inner: jobdbschema.WorkflowEngine{Engine: engine, Registry: rt.WorkflowRuntime.(jobdb.JobSchemaRegistry)}}
				jobCtx, gitCtx := GenerateTestContext()
				start := workflowctl.StartJob{TenantId: "timeout-repro", RecipeName: "timeout_repro_recipe", JobContext: jobCtx, GitRef: gitCtx.ParentRef}
				var embedded []recipe.Recipe
				var resolver RecipeSourceResolver
				var loads atomic.Int32
				if scope == "runtime-resolved-root" || scope == "timely-runtime-root" {
					resolver = NewRecipeSourceResolver(RecipeSourceResolverOptions{RecipeRefResolver: NewProviderBackedRecipeRefResolver(func(string, string) (*recipe.Recipe, error) { loads.Add(1); return rec, nil })})
				} else {
					embedded = []recipe.Recipe{*rec}
				}
				key, err := starter.StartRecipeJob(context.Background(), start, submitter, embedded...)
				require.NoError(t, err)
				wantTotal := 30 * time.Minute
				if scope == "preloaded-root" {
					wantTotal = budget
				}
				require.Equal(t, wantTotal, time.Duration(*submitter.policy.TotalTimeout))
				t.Logf("submitted durable job TotalTimeout=%s; lexical scope=%s", wantTotal, budget)
				output := newActivityOutputTaskData(t, gitCtx)
				finish := &timeoutReproActivity{name: "timeout_repro_finish:run", output: output}
				tasks := []jobworkflow.TaskWorker{finish, timeoutCheckpointWorker{}}
				if resolver != nil {
					tasks = append(tasks, newRootSourceResolutionTaskWorker(resolver))
				}
				started := time.Now()
				worker := NewRecipeJobWorker(RecipeJobWorkerOptions{RootSourceResolver: resolver})
				require.Equal(t, jobworkflow.JobRunSuspended, runTimeoutRepro(t, rt, key, worker, tasks...).Status)
				require.Zero(t, finish.runs.Load())
				h := rt.lastHandoff(t)
				require.Equal(t, "timeout_repro_input:wait", h.req.NextRoute.TaskType)
				task, err := engine.GetWaitingTask(context.Background(), key)
				require.NoError(t, err)
				// Spend part of the original budget waiting, then respond on time.
				// Recovery must retain the enclosing deadline when admitting the
				// next task, instead of granting it another full scope budget.
				waitTimeoutRepro(t, h.at.Add(250*time.Millisecond))
				require.NoError(t, task.Finish(context.Background(), output))
				require.Less(t, time.Since(started), budget, "external response must precede the original scope deadline")
				resolvedBeforeResume := loads.Load()
				// No active worker exists during this interval. New worker below
				// replays the durable results without repeating external completion.
				resumeAfter := budget + 150*time.Millisecond
				if scope == "timely-runtime-root" {
					resumeAfter = 300 * time.Millisecond
				}
				waitTimeoutRepro(t, h.at.Add(resumeAfter))
				worker = NewRecipeJobWorker(RecipeJobWorkerOptions{RootSourceResolver: resolver})
				runnable, err := jobworkflow.GetJobForRun(context.Background(), rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: tasks, WorkerID: "restart", LeaseDuration: time.Second})
				require.NoError(t, err)
				out, runErr := runnable.Run(nil)
				if scope == "preloaded-root" {
					require.NoError(t, runErr)
					requireTimeoutCompletion(t, rt, key, out)
					require.Zero(t, finish.runs.Load(), "root durable job timeout prevents new work")
				} else if scope == "completed-nested" || scope == "timely-runtime-root" {
					require.NoError(t, runErr)
					require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
					require.EqualValues(t, 1, finish.runs.Load())
				} else {
					require.NoError(t, runErr)
					require.Equal(t, jobworkflow.JobRunFailed, out.Status)
					require.ErrorContains(t, out.JobError, "timed out")
					requireTimeoutCompletion(t, rt, key, out)
					require.Zero(t, finish.runs.Load(), "unfinished task must not execute after the original deadline")
					info, err := rt.GetJob(context.Background(), key)
					require.NoError(t, err)
					require.Equal(t, jobdb.JobStatusCompleted, info.Status)
				}
				require.Equal(t, resolvedBeforeResume, loads.Load(), "resolved recipe is cached; replay should not call recipe provider again")
			})
		}
	}
}
