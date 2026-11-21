package recipetest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func modeSource(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	// A previous source may be read-only. Make it writable before updating
	// its bytes so these regressions also run without root privileges.
	if err := os.Chmod(path, 0600); !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	require.NoError(t, os.Chmod(path, mode))
}

func requireFixtureMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, mode, info.Mode().Perm(), path)
	require.Zero(t, info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, content, string(data))
}

func TestFixtureSourceModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprintf("directory=%t", directory), func(t *testing.T) {
			source, dest := t.TempDir(), t.TempDir()
			// Reuse each destination in both directions; map iteration cannot
			// determine which source replaces which.
			for _, mask := range []os.FileMode{0, 0111, 0, 0001, 0010, 0011, 0100, 0101, 0110} {
				modeSource(t, filepath.Join(source, "nested/file"), "new contents", 0400|mask|os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
				if directory {
					require.NoError(t, copyFixtureSource(dest, ".", source))
				} else {
					require.NoError(t, copyFixtureSource(dest, "nested/file", filepath.Join(source, "nested/file")))
				}
				requireFixtureMode(t, filepath.Join(dest, "nested/file"), "new contents", 0644|mask)
				// Change only the bytes so the next import replaces the mode
				// produced by this one (including 0644 -> executable).
				require.NoError(t, os.WriteFile(filepath.Join(dest, "nested/file"), []byte("old contents"), 0644))
			}
			modeSource(t, filepath.Join(source, "nested/file"), "writable source", 0666)
			if directory {
				require.NoError(t, copyFixtureSource(dest, ".", source))
			} else {
				require.NoError(t, copyFixtureSource(dest, "nested/file", filepath.Join(source, "nested/file")))
			}
			requireFixtureMode(t, filepath.Join(dest, "nested/file"), "writable source", 0644)
		})
	}
}

func TestFixtureSourceSafety(t *testing.T) {
	for _, directory := range []bool{false, true} {
		for _, kind := range []string{"source-link", "source-directory-link", "leaf-link", "parent-link", "absolute", "escape", "directory-conflict", "parent-conflict"} {
			t.Run(fmt.Sprintf("directory=%t/%s", directory, kind), func(t *testing.T) {
				source, dest, outside := t.TempDir(), t.TempDir(), t.TempDir()
				modeSource(t, filepath.Join(source, "file"), "source", 0755)
				modeSource(t, filepath.Join(outside, "file"), "outside", 0711)
				name := "file"
				switch kind {
				case "source-link":
					require.NoError(t, os.Remove(filepath.Join(source, "file")))
					require.NoError(t, os.Symlink(filepath.Join(outside, "file"), filepath.Join(source, "file")))
				case "source-directory-link":
					require.NoError(t, os.Remove(filepath.Join(source, "file")))
					require.NoError(t, os.Symlink(outside, filepath.Join(source, "file")))
				case "leaf-link":
					require.NoError(t, os.Symlink(filepath.Join(outside, "file"), filepath.Join(dest, "file")))
				case "parent-link":
					require.NoError(t, os.Symlink(outside, filepath.Join(dest, "parent")))
					name = "parent/file"
				case "absolute":
					name = filepath.Join(outside, "file")
				case "escape":
					name = "../outside/file"
				case "directory-conflict":
					require.NoError(t, os.Mkdir(filepath.Join(dest, "file"), 0755))
				case "parent-conflict":
					modeSource(t, filepath.Join(dest, "parent"), "parent", 0644)
					name = "parent/file"
				}
				if directory {
					// Import the member under the same invalid destination.
					if name != "file" {
						if kind == "absolute" || kind == "escape" {
							name = filepath.Dir(name)
						} else {
							name = "parent"
						}
					} else {
						name = "."
					}
					require.Error(t, copyFixtureSource(dest, name, source))
				} else {
					require.Error(t, copyFixtureSource(dest, name, filepath.Join(source, "file")))
				}
				requireFixtureMode(t, filepath.Join(outside, "file"), "outside", 0711)
			})
		}
	}
}

func TestFixtureSourceExcludesGitDirectories(t *testing.T) {
	source, dest := t.TempDir(), t.TempDir()
	for _, name := range []string{".git/config", "nested/.git/config", "file", "nested/file"} {
		modeSource(t, filepath.Join(source, name), name, 0644)
	}
	require.NoError(t, copyFixtureSource(dest, ".", source))
	for _, name := range []string{"file", "nested/file"} {
		requireFixtureMode(t, filepath.Join(dest, name), name, 0644)
	}
	require.NoDirExists(t, filepath.Join(dest, ".git"))
	require.NoDirExists(t, filepath.Join(dest, "nested/.git"))
}

func TestFixtureSourceInitialGitModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable modes")
	}
	source := t.TempDir()
	modeSource(t, filepath.Join(source, "tree/run"), "executable", 0755)
	modeSource(t, filepath.Join(source, "tree/data"), "ordinary", 0644)
	f := &runtimeFixture{root: t.TempDir(), opts: HarnessOptions{FixtureRoot: source}, cells: map[string]string{}, c: Case{Runtime: &RuntimeCase{Cells: map[string]CellFixture{"root": {
		Files:       map[string]string{"direct-run": "replaced inline"},
		FileSources: map[string]string{"direct-run": "tree/run", "direct-data": "tree/data", "imported": "tree"},
	}}}}}
	require.NoError(t, f.seedCells(context.Background(), "root"))
	for _, name := range []string{"direct-run", "direct-data", "imported/run", "imported/data"} {
		out, err := exec.Command("git", "-C", f.cells["root"], "ls-tree", "HEAD", "--", name).CombinedOutput()
		require.NoError(t, err, string(out))
		mode := "100644"
		if name == "direct-run" || name == "imported/run" {
			mode = "100755"
		}
		require.Contains(t, string(out), mode+" blob ")
	}
}
