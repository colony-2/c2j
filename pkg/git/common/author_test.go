package common

import (
	"context"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
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

func TestGitIdentityConfiguredFallbackPreservesOverrides(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx := context.Background()
	const fallback = "Recipe Test <recipe-test@example.com>"
	for _, tc := range []struct {
		name, config, committer string
	}{
		{"missing", "", fallback},
		{"local user", "[user]\nname = Local\nemail = local@example.com\n", "Local <local@example.com>"},
		{"partial user", "[user]\nname = Local\n", "Local <recipe-test@example.com>"},
		{"local committer", "[committer]\nname = Publisher\nemail = publisher@example.com\n", "Publisher <publisher@example.com>"},
		{"explicit bot", "[user]\nname = colony2.com\nemail = col2bot@colony2.com\n", DefaultGitAuthor},
		{"global user", "[user]\nname = Global\nemail = global@example.com\n", "Global <global@example.com>"},
		{"environment", "", "Environment <env@example.com>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			_, err := ExecuteGitCommand(ctx, repo, "init")
			require.NoError(t, err)
			config := filepath.Join(repo, ".git", "config")
			if tc.name == "global user" {
				config = filepath.Join(t.TempDir(), "gitconfig")
				t.Setenv("GIT_CONFIG_GLOBAL", config)
			}
			f, err := os.OpenFile(config, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			require.NoError(t, err)
			_, err = f.WriteString(tc.config)
			require.NoError(t, err)
			require.NoError(t, f.Close())
			if tc.name == "environment" {
				t.Setenv("GIT_COMMITTER_NAME", "Environment")
				t.Setenv("GIT_COMMITTER_EMAIL", "env@example.com")
			}
			require.NoError(t, EnsureGitIdentityWithFallback(ctx, repo, fallback))
			require.NoError(t, EnsureGitIdentityWithFallback(ctx, repo, fallback))
			_, err = ExecuteGitCommand(ctx, repo, "commit", "--allow-empty", "--author", fallback, "-m", "identity")
			require.NoError(t, err)
			out, err := ExecuteGitCommand(ctx, repo, "show", "-s", "--format=%an <%ae>|%cn <%ce>")
			require.NoError(t, err)
			require.Equal(t, fallback+"|"+tc.committer, strings.TrimSpace(string(out)))
		})
	}
}
