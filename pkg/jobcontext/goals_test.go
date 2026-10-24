package jobcontext

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGoalMetadataMergePreservesPairs(t *testing.T) {
	a := StartedJobContext{TenantID: "t", JobID: "a", GoalID: "g", RecipeName: "one"}
	b := StartedJobContext{TenantID: "t", JobID: "b", GoalID: "g", RecipeName: "two"}
	c := StartedJobContext{TenantID: "u", JobID: "a", GoalID: "other"}
	out, err := MergeStartedJobs(StartedJobsContext{Items: []StartedJobContext{a, b}}, StartedJobsContext{Items: []StartedJobContext{a, c}})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "a"}, out.JobIDs)
	require.Equal(t, []string{"g", "g", "other"}, out.GoalIDs)
	require.Equal(t, []StartedJobContext{a, b, c}, out.Items)
	conflict := a
	conflict.GoalID = "different"
	_, err = MergeStartedJobs(out, StartedJobsContext{Items: []StartedJobContext{conflict}})
	require.ErrorContains(t, err, "conflicting goal")
	incomplete := a
	incomplete.GoalID = ""
	_, err = MergeStartedJobs(StartedJobsContext{Items: []StartedJobContext{incomplete}})
	require.Error(t, err)
	empty, err := MergeStartedJobs()
	require.NoError(t, err)
	raw, err := json.Marshal(empty)
	require.NoError(t, err)
	require.JSONEq(t, `{"items":[],"job_ids":[],"goal_ids":[]}`, string(raw))
}
func TestCurrentGoalProtectedAndRequired(t *testing.T) {
	env := EnvForCurrent(Current{TenantID: "tenant", JobID: "child", GoalID: "goal"})
	env = MergeProtectedEnv(map[string]string{CurrentGoalIDEnv: "forged"}, env)
	current, present, err := CurrentFromEnv(func(k string) string { return env[k] })
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, "goal", current.GoalID)
	require.Equal(t, "goal", ParentFromCurrent(current).GoalID)
	delete(env, CurrentGoalIDEnv)
	_, _, err = CurrentFromEnv(func(k string) string { return env[k] })
	require.ErrorContains(t, err, CurrentGoalIDEnv)
}
