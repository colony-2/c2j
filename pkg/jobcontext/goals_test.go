package jobcontext

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExactJobContextMerge(t *testing.T) {
	a := StartedJobContext{TenantID: "t", JobID: "a", Cell: "app", CellID: "cell", CellRepo: "example.test/app", GoalID: "historical"}
	b := a
	b.JobID = "b"
	b.GoalID = ""
	out, err := MergeStartedJobs(StartedJobsContext{Items: []StartedJobContext{a, b}}, StartedJobsContext{Items: []StartedJobContext{a}})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, out.JobIDs)
	require.Empty(t, out.GoalIDs)
	require.Empty(t, out.Items[0].GoalID)
	for _, change := range []func(*StartedJobContext){func(v *StartedJobContext) { v.Cell = "other" }, func(v *StartedJobContext) { v.CellID = "other" }, func(v *StartedJobContext) { v.CellRepo = "example.test/other" }} {
		conflict := a
		change(&conflict)
		_, err = MergeStartedJobs(out, StartedJobsContext{Items: []StartedJobContext{conflict}})
		require.ErrorContains(t, err, "conflicting captured")
	}
	incomplete := a
	incomplete.JobID = ""
	_, err = MergeStartedJobs(StartedJobsContext{Items: []StartedJobContext{incomplete}})
	require.Error(t, err)
	raw, err := json.Marshal(EmptyStartedJobsContext())
	require.NoError(t, err)
	require.JSONEq(t, `{"items":[],"job_ids":[]}`, string(raw))
	var historical StartedJobsContext
	require.NoError(t, json.Unmarshal([]byte(`{"items":[{"tenant_id":"t","job_id":"a","goal_id":"G"}],"goal_ids":["G"]}`), &historical))
	require.Equal(t, "G", historical.Items[0].GoalID)
}

func TestExactJobContextEnvironment(t *testing.T) {
	env := EnvForCurrent(Current{TenantID: "tenant", JobID: "child", GoalID: "old"})
	env = MergeProtectedEnv(map[string]string{CurrentGoalIDEnv: "forged"}, env)
	current, present, err := CurrentFromEnv(func(k string) string { return env[k] })
	require.NoError(t, err)
	require.True(t, present)
	require.Empty(t, current.GoalID)
	require.Empty(t, ParentFromCurrent(current).GoalID)
	delete(env, CurrentGoalIDEnv)
	_, _, err = CurrentFromEnv(func(k string) string { return env[k] })
	require.NoError(t, err)
	env[CurrentGoalIDEnv] = "saved"
	current, _, err = CurrentFromEnv(func(k string) string { return env[k] })
	require.NoError(t, err)
	require.Equal(t, "saved", current.GoalID, "historical context remains decodable")
}
