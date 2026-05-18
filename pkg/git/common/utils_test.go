package common

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestIsRemoteRepository(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		expect bool
	}{
		{"https url", "https://github.com/org/repo.git", true},
		{"ssh url", "ssh://git@example.com/foo/bar.git", true},
		{"scp style", "git@github.com:org/repo.git", true},
		{"file url", "file:///tmp/repo.git", true},
		{"plain path", "/tmp/repo", false},
		{"relative path", "../repo", false},
	}

	for _, tc := range cases {
		if got := IsRemoteRepository(tc.input); got != tc.expect {
			t.Fatalf("%s: expected %v got %v", tc.name, tc.expect, got)
		}
	}
}

func TestExecuteGitCommandPreservesContextCauseWhenKilled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix shell fake git executable")
	}

	binDir := t.TempDir()
	fakeGit := filepath.Join(binDir, "git")
	if err := os.WriteFile(fakeGit, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	timer := time.AfterFunc(10*time.Millisecond, func() {
		cancel(context.DeadlineExceeded)
	})
	defer timer.Stop()

	_, err := ExecuteGitCommand(ctx, t.TempDir(), "status")
	if err == nil {
		t.Fatal("expected git command to fail after context cancellation")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded in error chain, got %v", err)
	}
}
