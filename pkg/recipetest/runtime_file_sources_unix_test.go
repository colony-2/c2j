//go:build unix

package recipetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFixtureSourceUmask(t *testing.T) {
	if os.Getenv("C2J_FIXTURE_UMASK_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestFixtureSourceUmask$", "-test.v")
		cmd.Env = append(os.Environ(), "C2J_FIXTURE_UMASK_CHILD=1")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return
	}
	previous := syscall.Umask(0077)
	defer syscall.Umask(previous)
	source, dest := t.TempDir(), t.TempDir()
	for _, directory := range []bool{false, true} {
		for _, mode := range []os.FileMode{0755, 0644, 0710} {
			modeSource(t, filepath.Join(source, "file"), "import", mode)
			if directory {
				require.NoError(t, copyFixtureSource(dest, ".", source))
			} else {
				require.NoError(t, copyFixtureSource(dest, "file", filepath.Join(source, "file")))
			}
			requireFixtureMode(t, filepath.Join(dest, "file"), "import", 0644|(mode&0111))
		}
		require.NoError(t, os.Remove(filepath.Join(dest, "file")))
	}
	// Inline and mock effects still share the original writer: creation is
	// umask-filtered, while replacement keeps the destination permissions.
	require.NoError(t, writeFixture(dest, "inline", []byte("inline")))
	requireFixtureMode(t, filepath.Join(dest, "inline"), "inline", 0600)
	modeSource(t, filepath.Join(dest, "inline"), "old", 0711)
	require.NoError(t, writeFixture(dest, "inline", []byte("effect")))
	requireFixtureMode(t, filepath.Join(dest, "inline"), "effect", 0711)
}

func TestFixtureSourceRejectsNonregular(t *testing.T) {
	source, dest := t.TempDir(), t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(source, "fifo"), 0600))
	require.ErrorContains(t, copyFixtureSource(dest, "fifo", filepath.Join(source, "fifo")), "regular file")
	require.ErrorContains(t, copyFixtureSource(dest, ".", source), "regular file")
	require.NoFileExists(t, filepath.Join(dest, "fifo"))
}
