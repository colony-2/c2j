package pathutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target with 'quotes'")
	require.NoError(t, os.Mkdir(target, 0700))
	alias := filepath.Join(dir, "alias")
	require.NoError(t, os.Symlink(target, alias))
	wd, err := os.Getwd()
	require.NoError(t, err)
	relative, err := filepath.Rel(wd, target)
	require.NoError(t, err)
	want, err := CanonicalExisting(target)
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(want))
	for _, path := range []string{target, relative, alias} {
		got, err := CanonicalExisting(path)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}

func TestCanonicalExistingRejectsMissing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing", "child")
	dangling := filepath.Join(dir, "dangling")
	require.NoError(t, os.Symlink(missing, dangling))
	for _, path := range []string{missing, dangling} {
		_, err := CanonicalExisting(path)
		require.Error(t, err)
	}
	_, err := os.Lstat(filepath.Dir(missing))
	require.True(t, os.IsNotExist(err), "lookup must not create directories")
	link, err := os.Readlink(dangling)
	require.NoError(t, err)
	require.Equal(t, missing, link)
}
