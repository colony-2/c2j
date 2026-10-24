package recipe

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/ops"
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
func TestGoalWaitContractAndBindings(t *testing.T) {
	tool := &bindingTool{fakeJobTool: fakeJobTool{key: jobdb.JobKey{TenantId: "t"}}}
	ctl := &fakeWorkflowControl{jobResultFunc: func(_ context.Context, key jobdb.JobKey) (jobdb.JobData, error) {
		require.Equal(t, "winner", key.JobId)
		return jobdb.NewTaskDataOrPanic(map[string]any{"output": map[string]any{"answer": 42}}), nil
	}}
	deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).WithWorkflowControl(ctl).Build()
	tool.call = func(d ...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
		require.Equal(t, []jobdb.Dependency{{Target: jobdb.DependencyTarget{GoalID: "g"}, Condition: jobdb.JobPrereqSuccess}}, d)
		return []jobdb.DependencyResolution{{Target: d[0].Target, State: jobdb.DependencySatisfied, SatisfiedByJobID: "winner"}}, nil
	}
	out, err := waitAndGetRecipeOutput(deps, context.Background(), StartedJob{JobID: "original", GoalID: "g"})
	require.NoError(t, err)
	require.Equal(t, ResultSource{Goal: "g", Job: "winner"}, out.Result)
	joined, err := waitAndNoResult(deps, context.Background(), StartedJobs{Items: []StartedJob{{JobID: "original", GoalID: "g"}}})
	require.NoError(t, err)
	require.Equal(t, "original", joined.Items[0].JobID)
	require.Equal(t, &out.Result, joined.Items[0].Result)
	for _, in := range []StartedJob{{GoalID: "g", Wait: WaitPolicy{Condition: "complete"}}, {Target: WaitTarget{Goal: "g"}, Wait: WaitPolicy{On: "job"}}, {Target: WaitTarget{Goal: "g", Job: "j"}}, {GoalID: "g", TenantID: "other"}, {GoalID: "g", Wait: WaitPolicy{On: "nonsense"}}} {
		_, err = awaitChild(deps, context.Background(), in, false)
		require.Error(t, err)
	}
	tool.call = func(...jobdb.Dependency) ([]jobdb.DependencyResolution, error) { return nil, context.DeadlineExceeded }
	_, err = awaitChild(deps, context.Background(), StartedJob{GoalID: "g"}, true)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
func TestChildGroupCollectsAfterExplicitGoalFailure(t *testing.T) {
	failure := jobdb.GoalFailure{Code: "goal_failed", GoalID: "failed", Message: "exhausted", TerminalJobIDs: []string{"a"}}
	tool := &bindingTool{fakeJobTool: fakeJobTool{key: jobdb.JobKey{TenantId: "t"}}}
	var seen []string
	tool.call = func(d ...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
		id := d[0].Target.GoalID
		seen = append(seen, id)
		if id == "failed" {
			return nil, &jobdb.GoalFailedError{Failure: failure}
		}
		return []jobdb.DependencyResolution{{Target: d[0].Target, State: jobdb.DependencySatisfied, SatisfiedByJobID: "replacement"}}, nil
	}
	deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).Build()
	state, err := awaitChildGroup(deps, context.Background(), ChildGroupStepState{Children: []ChildGroupChildRecord{{Status: "started", JobID: "a", GoalID: "failed", Required: false}, {Status: "started", JobID: "b", GoalID: "open", Required: true}}})
	require.NoError(t, err)
	require.Equal(t, []string{"failed", "open"}, seen)
	require.Equal(t, &failure, state.Children[0].GoalFailure)
	require.Equal(t, "replacement", state.Children[1].Result.Job)
}
