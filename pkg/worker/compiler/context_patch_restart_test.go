package compiler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Regression guard: when SWF replays a context_patch chapter (surfaced via TaskInputMismatchError),
// we apply the patch, then re-run the task with re-resolved inputs.
func TestExecuteOp2_ContextPatchReplayReResolvesInputs(t *testing.T) {
	originalOps := coreops.List()
	t.Cleanup(func() {
		coreops.Clear()
		if len(originalOps) > 0 {
			coreops.Register(originalOps...)
		}
	})
	coreops.Clear()

	opType := "context-patch-op"
	op, err := coreops.NewOp().
		WithType(opType).
		AddStep(opType, coreops.NewStepWithDeps(func(_ coreops.OpDependencies, _ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
			// Not executed in this test (we stub DoTask), but required for chain typing/validation.
			return map[string]interface{}{"ok": true, "author": in["author"]}, nil
		})).
		Build()
	require.NoError(t, err)
	coreops.Register(op.(coreops.RegisterableOp))

	jobCtx, gitCtx := GenerateTestContext()
	jobCtx.GitBase.GitAuthor = "old-author"

	patch := coretask.ContextPatch{
		Job: map[string]any{"git": map[string]any{"author": "new-author"}},
	}
	patchEnv, err := coretask.NewOutputEnvelope(coretask.OutputKindContextPatch, patch)
	require.NoError(t, err)
	patchTaskData := jobdb.NewTaskDataOrPanic(patchEnv)

	okOut := workerops.ActivityInvocationOutput{
		OpOutput: map[string]interface{}{"ok": true},
		GitResult: contextual.GitCommitContext{
			PersistHash: gitCtx.PersistHash,
			ParentHash:  gitCtx.ParentHash,
			ParentRef:   gitCtx.ParentRef,
		},
		NextTask: "",
	}
	okEnv, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, okOut)
	require.NoError(t, err)
	okTaskData := jobdb.NewTaskDataOrPanic(okEnv)

	stub := &patchingStubJobContext{
		jobKey:        jobdb.JobKey{TenantId: "test-tenant", JobId: "stub-job"},
		cachedPatch:   patchTaskData,
		successOutput: okTaskData,
	}

	ctx := workflow.Context{
		JobContext:           stub,
		ServiceDependencies2: coreops.NewServiceDepsBuilder().Build(),
	}

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeOp{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{
					ID: "patch-recipe",
					Inputs: map[string]interface{}{
						"author": "{{ context.git.author }}",
					},
				},
			},
			OpData: recipe.OpData{Op: opType},
		},
	}

	_, _, err = ExecuteRecipe(ctx, rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, []string{"old-author", "new-author"}, stub.seenAuthors)
	require.Equal(t, 2, stub.calls)
}

type patchingStubJobContext struct {
	jobKey        jobdb.JobKey
	calls         int
	seenAuthors   []string
	cachedPatch   jobdb.TaskData
	successOutput jobdb.TaskData
}

var _ jobworkflow.JobContext = &patchingStubJobContext{}

func (p *patchingStubJobContext) GetJobKey() jobdb.JobKey            { return p.jobKey }
func (p *patchingStubJobContext) Logger() *slog.Logger               { return slog.Default() }
func (p *patchingStubJobContext) AwaitDuration(jobdb.Duration) error { return nil }
func (p *patchingStubJobContext) AwaitJobs(jobIds ...string) error   { return nil }

func (p *patchingStubJobContext) DoTask(_ jobdb.RunPolicy, _ string, data jobdb.TaskData) (jobdb.TaskData, error) {
	p.calls++

	raw, err := data.GetData()
	if err == nil {
		var req workerops.ActivityInvocationRequest
		if jsonErr := json.Unmarshal(raw, &req); jsonErr == nil {
			if v, ok := req.Input["author"].(string); ok {
				p.seenAuthors = append(p.seenAuthors, v)
			}
		}
	}

	if p.calls == 1 {
		return nil, jobworkflow.TaskInputMismatchError{
			TaskType:        "stub",
			Ordinal:         1,
			CachedOutput:    p.cachedPatch,
			CachedInputHash: "cached",
		}
	}
	return p.successOutput, nil
}
