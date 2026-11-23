package recipe

import (
	"context"
	"errors"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
)

type goalStatusControl struct{ fakeWorkflowControl }

func (*goalStatusControl) GetGoal(context.Context, jobdb.GoalKey) (jobdb.GoalInfo, error) {
	panic("live goal query is forbidden")
}

func TestExactJobCurrentStatusAndReadErrors(t *testing.T) {
	for _, status := range []string{"running", "failed", "cancelled", "completed"} {
		ctl := &goalStatusControl{}
		ctl.inspectFunc = func(_ context.Context, k jobdb.JobKey) (workflowctl.JobInspection, error) {
			require.Equal(t, "original", k.JobId)
			return workflowctl.JobInspection{JobKey: k, GoalID: "passive", Status: status, Terminal: status != "running"}, nil
		}
		tool := &bindingTool{fakeJobTool: fakeJobTool{key: jobdb.JobKey{TenantId: "t"}}, call: func(...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
			t.Fatal("current status must not wait")
			return nil, nil
		}}
		deps := ops.NewOpDependenciesBuilder().WithWorkflowControl(ctl).WithJobTool(tool).Build()
		out, err := awaitResultSoft(deps, context.Background(), AwaitResultSoftInput{Target: WaitTarget{Job: "original"}, ReturnWhen: "current_status"})
		require.NoError(t, err)
		require.Equal(t, "original", out.Result.Job)
		require.Equal(t, status, out.Status)
		require.Empty(t, out.GoalID)
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("storage unavailable")} {
			ctl.inspectFunc = func(context.Context, jobdb.JobKey) (workflowctl.JobInspection, error) {
				return workflowctl.JobInspection{}, cause
			}
			_, err = awaitResultSoft(deps, context.Background(), AwaitResultSoftInput{Target: WaitTarget{Job: "original"}, ReturnWhen: "current_status"})
			require.ErrorIs(t, err, cause)
		}
	}
}
