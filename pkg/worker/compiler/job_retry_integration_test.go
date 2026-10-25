package compiler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestSubmittedRecipeRetryBudget(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"sqlite", "remote"} {
		for _, tc := range []struct {
			name     string
			attempts int32
			recover  bool
			calls    int
			status   wf.JobRunStatus
		}{
			{"default", 0, false, 1, wf.JobRunFailed},
			{"explicit-one", 1, false, 1, wf.JobRunFailed},
			{"explicit-three-exhausted", 3, false, 3, wf.JobRunFailed},
			{"explicit-three-recovers", 3, true, 2, wf.JobRunCompleted},
		} {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				rt := newTimeoutReproRuntime(t, backend)
				rec := probeRecipe(probeNode("work"))
				if tc.attempts > 0 {
					rec.RecipeImpl.(*recipe.RecipeSequence).Retry = &recipe.RetryPolicy{MaximumAttempts: tc.attempts, InitialInterval: jobdb.Duration(time.Millisecond)}
				}
				worker := NewRecipeJobWorker(RecipeJobWorkerOptions{})
				sdk, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, err)
				engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt.WorkflowRuntime.(jobdb.JobSchemaRegistry)}
				ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "retry-tests", RecipeName: "root"}, engine, rec)
				require.NoError(t, err)
				stored, err := engine.GetJob(ctx, ref.Key())
				require.NoError(t, err)
				require.Equal(t, int32(1), stored.ExecutionState.RunPolicy.Retry.MaximumAttempts, "recipe retry must not multiply an outer job retry")
				_, git := GenerateTestContext()
				probe := &scopeProbeTask{calls: map[string]int{}, git: git}
				var task wf.TaskWorker = publicationFaultWorker{TaskWorker: probe, after: func(jobdb.TaskData) error { return fmt.Errorf("retry budget probe") }}
				if tc.recover {
					probe.failOnce = "work"
					task = probe
				}
				result := probeRun(t, rt, ref.Key(), worker, task)
				require.Equal(t, tc.status, result.Status, "%v", result.JobError)
				require.Equal(t, tc.calls, probe.calls["work"])
				replayErr := probeReplay(t, rt, ref.Key(), worker)
				if tc.status == wf.JobRunCompleted {
					require.NoError(t, replayErr)
				} else {
					require.Error(t, replayErr)
				}
				require.Equal(t, tc.calls, probe.calls["work"], "replay must not execute another attempt")
			})
		}
	}
}
