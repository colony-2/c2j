package process

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/resourceretry"
	"github.com/stretchr/testify/require"
)

func TestResourceRetryRejectedStart(t *testing.T) {
	for _, eligible := range []bool{false, true} {
		ctx := context.WithValue(celltest.Context(context.Background()), startProcessKey{}, func(*exec.Cmd) error {
			return &os.PathError{Op: "fork/exec", Path: "/bin/sh", Err: syscall.EAGAIN}
		})
		capture := NewCapture(ctx)
		_, _, err := ExecuteProcess(ctx, RunRequest{ResourceRetry: eligible, Shell: "sh", Run: "touch must-not-run", WorkingDir: t.TempDir(), Capture: capture})
		require.Error(t, err)
		require.Equal(t, eligible, resourceretry.Is(err))
		require.True(t, capture.LaunchAttempted)
		require.False(t, capture.Launched)
	}
	ctx, cancel := context.WithCancel(celltest.Context(context.Background()))
	ctx = context.WithValue(ctx, startProcessKey{}, func(*exec.Cmd) error { cancel(); return syscall.EAGAIN })
	_, _, err := ExecuteProcess(ctx, RunRequest{ResourceRetry: true, Shell: "sh", Run: "true"})
	require.False(t, resourceretry.Is(err))
	_, _, err = ExecuteProcess(celltest.Context(context.Background()), RunRequest{ResourceRetry: true, Shell: "sh", Run: "printf 'fork/exec git: resource temporarily unavailable' >&2; exit 1"})
	require.Error(t, err)
	require.False(t, resourceretry.Is(err), "launched commands cannot authorize replay through diagnostic text")
}

func TestResourceRetryCapabilityEnvironment(t *testing.T) {
	t.Setenv(resourceretry.CapabilityEnv, "inherited-forgery")
	for _, supported := range []bool{false, true} {
		for _, eligible := range []bool{false, true} {
			ctx := resourceretry.WithSupport(celltest.Context(context.Background()), supported)
			ctx = WithToolEnvironment(ctx, "", map[string]string{resourceretry.CapabilityEnv: "tool-forgery"})
			ctx = WithUnsetEnv(ctx, []string{resourceretry.CapabilityEnv})
			out, _, err := ExecuteProcess(ctx, RunRequest{ResourceRetry: eligible, WorkingDir: t.TempDir(), Shell: "sh", Run: `printf '%s' "${C2J_RESOURCE_RETRY_CONTROL-absent}"`, Env: map[string]string{resourceretry.CapabilityEnv: "authored-forgery"}})
			require.NoError(t, err)
			want := "absent"
			if eligible && supported && CheckedTreeCleanupSupported {
				want = "1"
			}
			require.Equal(t, want, string(out))
		}
	}
}
