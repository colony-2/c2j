package executiondiag

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/stretchr/testify/require"
)

func TestPortableDirectoryAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "services", "api"), 0755))
	require.NoError(t, os.Symlink(external, filepath.Join(root, "escape")))
	r := New(context.Background(), ops.NewOpDependenciesBuilder().WithOperationPaths(ops.OperationPaths{Workdir: filepath.Dir(root), WorktreePath: root, Inbox: filepath.Join(root, "services")}).Build())
	require.Equal(t, "api (relative to inbox)", r.PortableDir(filepath.Join(root, "services", "api")))
	// Remove workdir since t.TempDir roots can share a parent.
	r.Roots = []Root{{"worktree", root}}
	require.Contains(t, r.PortableDir(filepath.Join(root, "escape")), "outside known task roots")
	require.Contains(t, r.PortableDir(filepath.Join(root, "missing")), "unavailable (requested: missing (relative to worktree)")
}
func TestPreviewBoundaries(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", PreviewLimit+1), strings.Repeat("€", PreviewLimit), strings.Repeat("\x00", PreviewLimit)} {
		preview, truncated := Preview(text, false, "_c2j/execution/check.log")
		require.True(t, truncated)
		require.LessOrEqual(t, len(preview), PreviewLimit)
		require.True(t, utf8.ValidString(preview))
		require.True(t, strings.HasSuffix(preview, "[Output truncated. See execution log: _c2j/execution/check.log]"))
		b, err := json.Marshal(preview)
		require.NoError(t, err)
		require.LessOrEqual(t, len(b), PreviewLimit)
	}
	small, truncated := Preview("ok\n", false, "log")
	require.False(t, truncated)
	require.Equal(t, "ok", small)
}
func TestSafeLogNames(t *testing.T) {
	for _, node := range []string{"scope/..", `scope/a\b`, "scope/a:b", strings.Repeat("x", 400)} {
		r := New(context.Background(), ops.NewOpDependenciesBuilder().WithGitContext(ops.GitExecutionContext{NodePath: node}).Build())
		require.Equal(t, "_c2j/execution", filepath.Dir(r.Name))
		require.Less(t, len(filepath.Base(r.Name)), 140)
	}
}

func TestRenderedLogLimitKeepsResultAndNotice(t *testing.T) {
	deps := ops.NewOpDependenciesBuilder().Build()
	r := New(context.Background(), deps)
	defer r.Close()
	_, _, err := process.ExecuteProcess(context.Background(), r.Prepare(process.RunRequest{WorkingDir: t.TempDir(), Shell: "sh", Run: `head -c 300000 /dev/zero | tr '\000' '\n'`}))
	require.NoError(t, err)
	r.Publish(nil, 0, false)
	raw, err := deps.GetOutputArtifacts()[0].Bytes(context.Background())
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), LogLimit)
	require.Contains(t, string(raw), "Execution log truncated: rendered log size limit exceeded")
	require.Contains(t, string(raw), "Result: exited with code 0")
	require.True(t, strings.HasSuffix(string(raw), "Operation result: completed\n"))
}
func TestDirectorySnapshotAndNormalizedBoundaries(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "api")
	require.NoError(t, os.Mkdir(dir, 0755))
	deps := ops.NewOpDependenciesBuilder().WithWorktreePath(root).Build()
	r := New(context.Background(), deps)
	defer r.Close()
	r.Prepare(process.RunRequest{WorkingDir: dir})
	require.NoError(t, os.Remove(dir))
	require.Equal(t, "api (relative to worktree)", r.Directory)
	display, normalized := r.display("cat " + root + "/test; echo " + root + "-external/test")
	require.True(t, normalized)
	require.Contains(t, display, "${worktree}/test")
	require.Contains(t, display, root+"-external/test")
}
