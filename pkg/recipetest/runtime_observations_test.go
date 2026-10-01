package recipetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestObserveWorktreeThroughSymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	require.NoError(t, os.Mkdir(root, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "candidate"), []byte("retained"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "large"), []byte(strings.Repeat("x", 65537)), 0644))
	alias := filepath.Join(base, "alias")
	require.NoError(t, os.Symlink(root, alias))
	got, err := observeWorktree(alias, []string{"candidate", "absent", "large"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"candidate": "retained"}, got)
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("not in worktree"), 0644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
	_, err = observeWorktree(alias, []string{"escape"})
	require.ErrorContains(t, err, "escapes worktree")
	_, err = observeWorktree(alias, []string{"../outside"})
	require.ErrorContains(t, err, "remain within")
}
