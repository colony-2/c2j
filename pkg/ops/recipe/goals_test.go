package recipe

import (
	"context"
	"errors"
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type bindingTool struct {
	fakeJobTool
	call func(...jobdb.Dependency) ([]jobdb.DependencyResolution, error)
}

func (t *bindingTool) AwaitDependencies(d ...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
	return t.call(d...)
}

// A saved binding naming B remains the artifact authority even if B's primary
// goal differs or A later succeeds. No live goal read or membership lookup.
func TestExactJobHistoricalProducer(t *testing.T) {
	ctx := context.Background()
	key := jobdb.ArtifactKey{JobId: "B", TaskOrdinal: 9, Name: "result.txt", SizeBytes: 6}
	artifact := jobdb.NewArtifactFromBytes(key.Name, []byte("from B"))
	jobdb.AssignArtifactKey(artifact, key)
	witness := jobdb.DependencyResolution{Target: jobdb.DependencyTarget{GoalID: "G"}, State: jobdb.DependencySatisfied, SatisfiedByJobID: "B"}
	saved := witness
	ctl := &goalStatusControl{}
	ctl.jobResultFunc = func(_ context.Context, k jobdb.JobKey) (jobdb.JobData, error) {
		require.Equal(t, "B", k.JobId)
		return jobdb.NewTaskDataOrPanic(map[string]any{"output": map[string]any{"producer": "B", "goal_id": "opaque"}}, artifact), nil
	}
	deps := ops.NewOpDependenciesBuilder().WithWorkflowControl(ctl).WithJobTool(&fakeJobTool{key: jobdb.JobKey{TenantId: "t"}}).Build()
	out, err := getRecipeOutput(deps, ctx, StartedJob{JobID: witness.SatisfiedByJobID})
	require.NoError(t, err)
	require.Equal(t, "B", out.Result.Job)
	require.Equal(t, "opaque", out.Outputs["goal_id"])
	require.Equal(t, recipeartifacts.NewStoredRef(key), out.Artifacts[key.Name])
	require.Equal(t, saved, witness)
	for _, cause := range []error{jobdb.ErrJobNotFound, errors.New("forbidden"), errors.New("corrupt producer")} {
		ctl.jobResultFunc = func(_ context.Context, k jobdb.JobKey) (jobdb.JobData, error) {
			require.Equal(t, "B", k.JobId)
			return nil, cause
		}
		_, err = getRecipeOutput(deps, ctx, StartedJob{JobID: witness.SatisfiedByJobID})
		require.ErrorIs(t, err, cause)
	}
}

func TestExactJobAggregateTerminalPartial(t *testing.T) {
	key := jobdb.ArtifactKey{JobId: "failed", TaskOrdinal: 7, Name: "partial.txt"}
	artifact := jobdb.NewArtifactFromBytes(key.Name, []byte("partial"))
	jobdb.AssignArtifactKey(artifact, key)
	for _, required := range []bool{false, true} {
		ctl := &fakeWorkflowControl{inspectFunc: func(_ context.Context, k jobdb.JobKey) (workflowctl.JobInspection, error) {
			return workflowctl.JobInspection{JobKey: k, Terminal: true, Status: "failed", FailureMessage: "original failure", Output: jobdb.NewTaskDataOrPanic(map[string]any{"output": map[string]any{"partial": true}}, artifact)}, nil
		}}
		deps := ops.NewOpDependenciesBuilder().WithWorkflowControl(ctl).WithJobTool(&fakeJobTool{key: jobdb.JobKey{TenantId: "t"}}).Build()
		out, err := collectChildGroup(deps, context.Background(), ChildGroupStepState{Children: []ChildGroupChildRecord{{Key: "one", Required: required, JobID: "failed", Status: "started", Result: ResultSource{Job: "failed"}}}})
		require.NoError(t, err)
		require.Equal(t, !required, out.Ok)
		require.Equal(t, 1, out.Summary.Failed)
		require.Equal(t, true, out.Children[0].Outputs["partial"])
		require.Equal(t, "failed", out.Children[0].Artifacts[key.Name].Stored.Key.JobId)
		require.Equal(t, "original failure", out.Children[0].Error)
	}
}
