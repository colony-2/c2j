package common

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func resourceRetryFakeGit(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix shell fake git executable")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// These checks preserve evidence for the scheduler integration. They do not
// classify the diagnostic or authorize replay of the caller's operation.
func TestResourceRetryValidationPreservesFailure(t *testing.T) {
	for _, diagnostic := range []string{
		"fatal: cannot fork: Resource temporarily unavailable",
		"fatal: Authentication failed",
		"fatal: not a git repository",
	} {
		t.Run(diagnostic, func(t *testing.T) {
			resourceRetryFakeGit(t, "#!/bin/sh\nprintf '%s\\n' \"$C2J_TEST_GIT_DIAGNOSTIC\" >&2\nexit 128\n")
			t.Setenv("C2J_TEST_GIT_DIAGNOSTIC", diagnostic)
			err := ValidateRepositoryContext(context.Background(), t.TempDir())
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 128 {
				t.Fatalf("expected original Git exit status, got %v", err)
			}
			if !strings.Contains(err.Error(), diagnostic) {
				t.Fatalf("missing preparation diagnostic %q: %v", diagnostic, err)
			}
		})
	}
}

func TestResourceRetryValidationPreservesStartCause(t *testing.T) {
	resourceRetryFakeGit(t, "#!/c2j-missing-test-interpreter\n")
	err := ValidateRepositoryContext(context.Background(), t.TempDir())
	var start *os.PathError
	if !errors.As(err, &start) || start.Op != "fork/exec" {
		t.Fatalf("expected original process-start error, got %v", err)
	}
}

func TestResourceRetryValidationPreservesCancellation(t *testing.T) {
	resourceRetryFakeGit(t, "#!/bin/sh\nprintf '.git\\n'\n")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(context.DeadlineExceeded)
	if err := ValidateRepositoryContext(ctx, t.TempDir()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancellation cause to veto replay, got %v", err)
	}
}

func TestResourceRetryValidationSuccess(t *testing.T) {
	resourceRetryFakeGit(t, "#!/bin/sh\nprintf '.git\\n'\n")
	if err := ValidateRepositoryContext(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
