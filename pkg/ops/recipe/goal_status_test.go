package recipe

import (
	"context"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type goalStatusControl struct {
	fakeWorkflowControl
	goal jobdb.GoalInfo
	err  error
}

func (c *goalStatusControl) GetGoal(context.Context, jobdb.GoalKey) (jobdb.GoalInfo, error) {
	return c.goal, c.err
}

func TestSoftGoalCurrentStatusAndResultLookup(t *testing.T) {
	for _, state := range []jobdb.GoalState{jobdb.GoalOpen, jobdb.GoalFailed, jobdb.GoalSucceeded} {
		t.Run(string(state), func(t *testing.T) {
			tool := &bindingTool{fakeJobTool: fakeJobTool{key: jobdb.JobKey{TenantId: "tenant"}}, call: func(...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
				t.Fatal("current status must not wait")
				return nil, nil
			}}
			ctl := &goalStatusControl{goal: jobdb.GoalInfo{GoalKey: jobdb.GoalKey{TenantId: "tenant", GoalID: "goal"}, State: state}}
			if state == jobdb.GoalFailed {
				ctl.goal.Failure = &jobdb.GoalFailure{Code: "goal_failed", Message: "stopped"}
			}
			if state == jobdb.GoalSucceeded {
				ctl.goal.SuccessfulJobID = "replacement"
			}
			ctl.inspectFunc = func(_ context.Context, k jobdb.JobKey) (workflowctl.JobInspection, error) {
				require.Equal(t, "original", k.JobId)
				return workflowctl.JobInspection{GoalID: "goal"}, nil
			}
			ctl.jobResultFunc = func(_ context.Context, k jobdb.JobKey) (jobdb.JobData, error) {
				require.Equal(t, "replacement", k.JobId)
				return jobdb.NewTaskDataOrPanic(map[string]any{"output": map[string]any{"value": "winner"}}), nil
			}
			deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).WithWorkflowControl(ctl).Build()
			out, err := awaitResultSoft(deps, context.Background(), AwaitResultSoftInput{Target: WaitTarget{Job: "original"}, ReturnWhen: "current_status"})
			require.NoError(t, err)
			require.Equal(t, state, out.GoalState)
			require.Equal(t, state != jobdb.GoalOpen, out.Terminal)
			if state == jobdb.GoalSucceeded {
				require.Equal(t, ResultSource{Goal: "goal", Job: "replacement"}, out.Result)
				require.Equal(t, "winner", out.Outputs["value"])
			} else {
				require.Zero(t, ctl.jobResultCalls)
			}
			if state == jobdb.GoalFailed {
				require.Equal(t, ctl.goal.Failure, out.GoalFailure)
			}
			// Explicit result retrieval must choose the winner, and reject open/failed goals.
			result, err := getRecipeOutput(deps, context.Background(), StartedJob{Target: WaitTarget{Goal: "goal"}})
			if state == jobdb.GoalSucceeded {
				require.NoError(t, err)
				require.Equal(t, ResultSource{Goal: "goal", Job: "replacement"}, result.Result)
			} else {
				require.Error(t, err)
			}
			if state == jobdb.GoalFailed {
				require.ErrorIs(t, err, jobdb.ErrGoalFailed)
			}
		})
	}
}

func TestSoftGoalWaitPropagatesControlAndStorageErrors(t *testing.T) {
	for _, failure := range []error{context.DeadlineExceeded, context.Canceled, errors.New("storage unavailable")} {
		t.Run(failure.Error(), func(t *testing.T) {
			tool := &bindingTool{fakeJobTool: fakeJobTool{key: jobdb.JobKey{TenantId: "tenant"}}, call: func(...jobdb.Dependency) ([]jobdb.DependencyResolution, error) { return nil, failure }}
			ctl := &goalStatusControl{err: failure}
			deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).WithWorkflowControl(ctl).Build()
			for _, when := range []string{"terminal", "current_status"} {
				_, err := awaitResultSoft(deps, context.Background(), AwaitResultSoftInput{Target: WaitTarget{Goal: "goal"}, ReturnWhen: when})
				require.ErrorIs(t, err, failure)
			}
			require.Zero(t, ctl.jobResultCalls)
		})
	}
}
