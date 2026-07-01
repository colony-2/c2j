package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestTransformOperationPathsDefaultsForNone(t *testing.T) {
	host := testOperationPaths(t)

	result, err := TransformOperationPaths(context.Background(), nil, host)
	require.NoError(t, err)

	require.Equal(t, host, result.Runtime.Views.Host)
	require.Equal(t, host, result.Runtime.Views.Op)
	require.Empty(t, result.Runtime.Mounts)
	require.Equal(t, host.Inbox, result.Replacements[contextual.OpArtifactInboxSentinel])
	require.Equal(t, host.Outbox, result.Replacements[contextual.OpArtifactOutboxSentinel])
	require.Equal(t, host.WorktreePath, result.Replacements[contextual.OpWorktreePathSentinel])
	require.Equal(t, host.Workdir, result.Replacements[contextual.OpWorkdirPathSentinel])
}

func TestTransformOperationPathsDefaultsForShai(t *testing.T) {
	host := testOperationPaths(t)

	result, err := TransformOperationPaths(context.Background(), map[string]interface{}{"type": "shai"}, host)
	require.NoError(t, err)

	require.Equal(t, host, result.Runtime.Views.Host)
	require.Equal(t, ops.OperationPaths{
		Workdir:      DefaultShaiWorkdir,
		WorktreePath: DefaultShaiWorktreePath,
		Inbox:        DefaultShaiInbox,
		Outbox:       DefaultShaiOutbox,
	}, result.Runtime.Views.Op)
	require.Equal(t, DefaultShaiInbox, result.Replacements[contextual.OpArtifactInboxSentinel])
	require.Equal(t, DefaultShaiOutbox, result.Replacements[contextual.OpArtifactOutboxSentinel])
	require.Equal(t, DefaultShaiWorktreePath, result.Replacements[contextual.OpWorktreePathSentinel])
	require.Equal(t, DefaultShaiWorkdir, result.Replacements[contextual.OpWorkdirPathSentinel])
	require.Len(t, result.Runtime.Mounts, 4)
}

func TestParseSandboxInputRejectsUnknownFields(t *testing.T) {
	_, err := ParseSandboxInput(map[string]interface{}{
		"type":          "shai",
		"inline_config": map[string]interface{}{"x": "y"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "inline_config")
}

func TestTransformOperationPathsRejectsOpSentinelInSandboxConfig(t *testing.T) {
	host := testOperationPaths(t)

	_, err := TransformOperationPaths(context.Background(), map[string]interface{}{
		"type": "shai",
		"paths": map[string]interface{}{
			"inbox": map[string]interface{}{
				"sandbox": contextual.OpArtifactInboxSentinel,
			},
		},
	}, host)
	require.Error(t, err)
	require.Contains(t, err.Error(), "context.environment.op")
}

func TestShaiAppendResourceSetSkipsMountsCoveredByWorkspaceRoot(t *testing.T) {
	host := testOperationPaths(t)
	result, err := TransformOperationPaths(context.Background(), map[string]interface{}{"type": "shai"}, host)
	require.NoError(t, err)

	appendSet, err := shaiAppendResourceSet(result.Runtime.Mounts, nil, host.Workdir, DefaultShaiWorkdir)
	require.NoError(t, err)
	require.NotNil(t, appendSet)
	require.Len(t, appendSet.Mounts, 1)
	require.Equal(t, host.WorktreePath, appendSet.Mounts[0].Source)
	require.Equal(t, DefaultShaiWorktreePath, appendSet.Mounts[0].Target)
	require.Equal(t, ops.MountModeReadWrite, appendSet.Mounts[0].Mode)
}

func TestShaiAppendResourceSetRejectsConflictingDuplicateTargets(t *testing.T) {
	root := t.TempDir()
	_, err := shaiAppendResourceSet([]ops.RequiredMount{
		{Source: filepath.Join(root, "a"), Target: "/src/inbox", Mode: ops.MountModeReadWrite},
		{Source: filepath.Join(root, "b"), Target: "/src/inbox", Mode: ops.MountModeReadWrite},
	}, nil, root, DefaultShaiWorkdir)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "duplicate mount target"), err.Error())
}

func TestShaiAppendResourceSetIncludesPorts(t *testing.T) {
	root := t.TempDir()
	appendSet, err := shaiAppendResourceSet(nil, []ops.RequiredPort{
		{Host: "host.docker.internal", Port: 34567},
		{Host: "host.docker.internal", Port: 34567},
		{Host: "", Port: 2222},
	}, root, DefaultShaiWorkdir)
	require.NoError(t, err)
	require.NotNil(t, appendSet)
	require.Empty(t, appendSet.Mounts)
	require.Len(t, appendSet.Ports, 1)
	require.Equal(t, "host.docker.internal", appendSet.Ports[0].Host)
	require.Equal(t, 34567, appendSet.Ports[0].Port)
}

func TestExecuteProcessTimeoutKillsHostProcessTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell process-tree behavior")
	}

	root := t.TempDir()
	marker := filepath.Join(root, "child-survived")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, _, err := ExecuteProcess(ctx, RunRequest{
		WorkingDir: root,
		Shell:      "sh",
		Run:        "(sleep 1; echo leaked > child-survived) & sleep 5",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded), "expected deadline exceeded error, got %v", err)
	require.Less(t, time.Since(started), 2*time.Second)

	time.Sleep(1200 * time.Millisecond)
	_, statErr := os.Stat(marker)
	require.True(t, os.IsNotExist(statErr), "descendant process wrote marker after timeout")
}

func TestExecuteProcessUsesNonLoginBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script fake bash")
	}

	binDir := t.TempDir()
	fakeBash := filepath.Join(binDir, "bash")
	script := `#!/bin/sh
if [ "$1" = "-lc" ]; then
  printf 'profile noise\n' >&2
fi
if [ "$1" != "-c" ] && [ "$1" != "-lc" ]; then
  printf 'unexpected shell args: %s\n' "$*" >&2
  exit 64
fi
shift
eval "$1"
`
	require.NoError(t, os.WriteFile(fakeBash, []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, err := ExecuteProcess(context.Background(), RunRequest{
		WorkingDir: t.TempDir(),
		Shell:      "bash",
		Run:        "printf ok",
	})
	require.NoError(t, err)
	require.Equal(t, "ok", string(stdout))
	require.Empty(t, string(stderr))
}

func testOperationPaths(t *testing.T) ops.OperationPaths {
	t.Helper()
	root := t.TempDir()
	return ops.OperationPaths{
		Workdir:      root,
		WorktreePath: filepath.Join(root, "worktree"),
		Inbox:        filepath.Join(root, "inbox"),
		Outbox:       filepath.Join(root, "outbox"),
	}
}
