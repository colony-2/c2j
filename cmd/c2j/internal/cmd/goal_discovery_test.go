package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

func TestGoalDiscoveryCommands(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
	server := httptest.NewServer(remote.NewServer(db))
	t.Cleanup(server.Close)
	submit := func(id, repo string, deps ...jobdb.Dependency) jobdb.JobHandle {
		t.Helper()
		meta, err := json.Marshal(map[string]string{"repo": repo, "cell_name": "root"})
		require.NoError(t, err)
		out, err := db.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "t", JobID: id, JobType: "external", Metadata: meta, Data: jobdb.NewTaskDataOrPanic(map[string]any{}), Dependencies: deps}})
		require.NoError(t, err)
		return out
	}
	a := submit("a", "repo-one")
	submit("b", "repo-two")
	consumer := submit("consumer", "repo-two", jobdb.Dependency{Target: jobdb.DependencyTarget{GoalID: a.GoalID}, Condition: jobdb.JobPrereqSuccess})
	run := func(args ...string) ([]byte, error) {
		cmd := newGoalsCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"--jobdb", server.URL + "/t"}, args...))
		err := cmd.ExecuteContext(ctx)
		return out.Bytes(), err
	}
	raw, err := run("list", "--repo", "repo-one", "--cell", "root", "--job-type", "external", "--state", "open", "--created-after", "2020-01-01T00:00:00Z")
	require.NoError(t, err)
	var goals jobdb.ListGoalsResponse
	require.NoError(t, json.Unmarshal(raw, &goals))
	require.Len(t, goals.Goals, 1)
	require.Equal(t, a.GoalID, goals.Goals[0].GoalKey.GoalID)
	raw, err = run("list", "--page-size", "1")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &goals))
	require.NotEmpty(t, goals.NextPageToken)
	raw, err = run("list", "--page-size", "2", "--page-token", goals.NextPageToken)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &goals))
	require.Len(t, goals.Goals, 2)
	raw, err = run("waits", "--target-goal", a.GoalID)
	require.NoError(t, err)
	var waits jobdb.ListDependencyWaitsResponse
	require.NoError(t, json.Unmarshal(raw, &waits))
	require.Len(t, waits.Waits, 1)
	require.Equal(t, consumer.JobKey, waits.Waits[0].JobKey)
	raw, err = run("wait", consumer.JobKey.JobId, waits.Waits[0].WaitID)
	require.NoError(t, err)
	var wait jobdb.DependencyWaitInfo
	require.NoError(t, json.Unmarshal(raw, &wait))
	require.Equal(t, waits.Waits[0], wait)
	raw, err = run("waits", "--consumer-job", consumer.JobKey.JobId, "--state", "pending")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &waits))
	require.Len(t, waits.Waits, 1)
	for _, args := range [][]string{
		{"list", "--created-after", "invalid"}, {"list", "--page-size", "-1"}, {"list", "--state", "nonsense"},
		{"waits", "--target-goal", a.GoalID, "--target-job", "a"}, {"waits", "--target-goal", a.GoalID, "--state", "satisfied"}, {"waits"},
	} {
		_, err = run(args...)
		require.Error(t, err, "args: %v", args)
	}

	// Exercise lifecycle writes through the actual CLI/HTTP path, including retries.
	for i := 0; i < 2; i++ {
		_, err = run("attach", a.GoalID, "b")
		require.NoError(t, err)
	}
	raw, err = run("candidates", a.GoalID, "--page-size", "1")
	require.NoError(t, err)
	var candidates joblist.Page
	require.NoError(t, json.Unmarshal(raw, &candidates))
	require.Len(t, candidates.Jobs, 1)
	require.NotEmpty(t, candidates.NextPageToken)
	firstID := candidates.Jobs[0].JobID
	raw, err = run("candidates", a.GoalID, "--page-size", "1", "--page-token", candidates.NextPageToken)
	require.NoError(t, err)
	candidates = joblist.Page{}
	require.NoError(t, json.Unmarshal(raw, &candidates))
	require.Len(t, candidates.Jobs, 1)
	require.NotEqual(t, firstID, candidates.Jobs[0].JobID)
	require.Empty(t, candidates.NextPageToken)
	_, err = run("fail", a.GoalID)
	require.Error(t, err)
	_, err = run("fail", a.GoalID, "--message", " ")
	require.Error(t, err)
	for i := 0; i < 2; i++ {
		_, err = run("fail", a.GoalID, "--message", "no more attempts")
		require.NoError(t, err)
	}
	raw, err = run("get", a.GoalID)
	require.NoError(t, err)
	var failed jobdb.GoalInfo
	require.NoError(t, json.Unmarshal(raw, &failed))
	require.Equal(t, jobdb.GoalFailed, failed.State)
	require.Equal(t, "no more attempts", failed.Failure.Message)
	_, err = run("attach", a.GoalID, consumer.JobKey.JobId)
	require.Error(t, err)
	raw, err = run("waits", "--consumer-job", consumer.JobKey.JobId, "--state", "failed")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &waits))
	require.Len(t, waits.Waits, 1)
	require.Equal(t, failed.Failure, waits.Waits[0].Failure)
}
