package common

import (
	"context"
	"github.com/stretchr/testify/require"
	"os"
	"strings"
	"testing"
)

func TestGitIdentityFallbackAndOverrides(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx := context.Background()
	for _, explicit := range []bool{false, true} {
		repo := t.TempDir()
		_, err := ExecuteGitCommand(ctx, repo, "init")
		require.NoError(t, err)
		want := DefaultGitAuthor
		if explicit {
			_, err = ExecuteGitCommand(ctx, repo, "config", "user.name", "Configured")
			require.NoError(t, err)
			_, err = ExecuteGitCommand(ctx, repo, "config", "user.email", "configured@example.test")
			require.NoError(t, err)
			want = "Configured <configured@example.test>"
		}
		require.NoError(t, EnsureGitIdentity(ctx, repo))
		require.NoError(t, EnsureGitIdentity(ctx, repo))
		_, err = ExecuteGitCommand(ctx, repo, "commit", "--allow-empty", "-m", "test")
		require.NoError(t, err)
		out, err := ExecuteGitCommand(ctx, repo, "show", "-s", "--format=%an <%ae>|%cn <%ce>", "HEAD")
		require.NoError(t, err)
		require.Equal(t, want+"|"+want, strings.TrimSpace(string(out)))
		_, err = ExecuteGitCommand(ctx, repo, "commit", "--allow-empty", "--author", "Original <original@example.test>", "-m", "explicit author")
		require.NoError(t, err)
		out, err = ExecuteGitCommand(ctx, repo, "show", "-s", "--format=%an <%ae>|%cn <%ce>", "HEAD")
		require.NoError(t, err)
		require.Equal(t, "Original <original@example.test>|"+want, strings.TrimSpace(string(out)))
	}
	for _, cell := range []string{"", ".", "cells/example"} {
		require.Equal(t, DefaultGitAuthor, GitAuthorForCell(cell))
	}
}
