package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/git"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/metricsruntime"
	"github.com/stretchr/testify/require"
)

func TestGitRequestMetricsOncePerPullAndFetch(t *testing.T) {
	origin := t.TempDir()
	work := filepath.Join(t.TempDir(), "clone")
	run := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, e := c.CombinedOutput()
		require.NoError(t, e, string(out))
	}
	run(origin, "init", "-b", "main")
	run(origin, "commit", "--allow-empty", "-m", "initial")
	run(origin, "clone", origin, work)
	repo := git.NewRepository(git.Config{})
	ctx, timing := metricsruntime.Begin(context.Background(), "test:run", nil)
	metricsruntime.Execution(ctx)
	_, e := repo.Pull(ctx, work, git.PullOptions{Remote: "origin", Branch: "main", FastForward: true})
	require.NoError(t, e)
	require.NoError(t, repo.Fetch(ctx, work, git.FetchOptions{Remote: "origin"}))
	require.Error(t, repo.Fetch(ctx, work, git.FetchOptions{Remote: "missing"}))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.Error(t, repo.Fetch(canceled, work, git.FetchOptions{Remote: "origin"}))
	channels := timing.Finish(nil)
	result, e := metrics.Reduce([]metrics.Record{{Channel: "runtime", Task: `/scope:"$job"#1/attempt:1/task:"run"#1`, Attempt: 1, Ordinal: 1, ChannelReport: channels.Runtime}}, metrics.Query{}, 1)
	require.NoError(t, e)
	observations := map[string]int64{}
	for _, p := range result.Snapshot.Series["c2j_request_duration_seconds"] {
		observations[p.Labels["operation"]+"/"+p.Labels["outcome"]] = p.Count
		require.Len(t, p.Labels, 2)
	}
	require.Equal(t, map[string]int64{"git.pull/succeeded": 1, "git.fetch/succeeded": 1, "git.fetch/failed": 1, "git.fetch/canceled": 1}, observations)
}
