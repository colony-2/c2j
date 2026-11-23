package recipe

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/jobsemantics"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExactJobWaitPolicies(t *testing.T) {
	for _, tc := range []struct {
		policy  WaitPolicy
		result  bool
		want    WaitPolicy
		invalid bool
	}{
		{want: WaitPolicy{On: "job", Condition: "complete"}},
		{result: true, want: WaitPolicy{On: "job", Condition: "success"}},
		{policy: WaitPolicy{Condition: "success"}, want: WaitPolicy{On: "job", Condition: "success"}},
		{policy: WaitPolicy{On: "goal"}, invalid: true},
		{policy: WaitPolicy{Condition: "complete"}, result: true, invalid: true},
		{policy: WaitPolicy{Condition: "invalid"}, invalid: true},
	} {
		got, err := tc.policy.normalized(tc.result)
		if tc.invalid {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		}
	}
	tool := &fakeJobTool{key: jobdb.JobKey{TenantId: "t", JobId: "consumer"}}
	deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).Build()
	// A legacy goal on an otherwise exact handle is passive historical data.
	binding, err := awaitChild(deps, context.Background(), StartedJob{JobID: "A", GoalID: "legacy-G"}, false)
	require.NoError(t, err)
	require.Equal(t, jobdb.DependencyTarget{JobID: "A"}, binding.Target)
	for _, input := range []StartedJob{{JobID: "A", Wait: WaitPolicy{On: "goal"}}, {Target: WaitTarget{Goal: "G"}}, {JobID: "A", TenantID: "other"}} {
		before := len(tool.awaitArgs)
		_, err = awaitChild(deps, context.Background(), input, false)
		require.Error(t, err)
		require.Len(t, tool.awaitArgs, before)
	}
}

func TestExactJobChildGroupSavedPolicy(t *testing.T) {
	// The same retained command must keep its old explicit exact-job policy.
	command := []byte(`{"mode":"run_and_get_result","wait":{"on":"job"},"children":[{"key":"skipped","recipe":"build","skipped":true}]}`)
	for _, version := range []int{0, 1, 2} {
		var input ChildGroupStartInput
		require.NoError(t, json.Unmarshal(command, &input))
		deps := ops.NewOpDependenciesBuilder().WithJobTool(&fakeJobTool{key: jobdb.JobKey{TenantId: "t", JobId: "parent"}}).Build()
		state, err := startChildGroup(deps, jobsemantics.WithVersion(context.Background(), version), input)
		require.NoError(t, err)
		require.Len(t, state.Children, 1)
		condition := "complete"
		if version == 2 {
			condition = "success"
		}
		require.Equal(t, WaitPolicy{On: "job", Condition: condition}, state.Children[0].Wait)
	}
}
