package extensionfuncs

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
)

func TestCapturedPackageModesLinksAndIntegrity(t *testing.T) {
	for _, mode := range []string{"valid", "valid-aliased-temp", "path", "link-escape", "link-chain-escape", "link-parent", "missing-mode", "invalid-mode", "duplicate", "missing"} {
		t.Run(mode, func(t *testing.T) {
			snapshot := recipe.FunctionPackageSnapshot{Selector: "selector", ResolvedSelector: "pinned", PackageDir: "tools", Files: map[string]string{"tools/run.sh": base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nprintf captured"))}, FileModes: map[string]uint32{"tools/run.sh": 0755}, Symlinks: map[string]string{"tools/run-link": "run.sh"}}
			switch mode {
			case "valid-aliased-temp":
				// Reproduce macOS's /var -> /private/var temporary-directory alias.
				parent := t.TempDir()
				realTemp := filepath.Join(parent, "real")
				require.NoError(t, os.Mkdir(realTemp, 0700))
				alias := filepath.Join(parent, "alias")
				require.NoError(t, os.Symlink(realTemp, alias))
				t.Setenv("TMPDIR", alias)
			case "path":
				snapshot.Files["../escape"] = ""
			case "link-escape":
				snapshot.Symlinks["escape"] = "../outside"
			case "link-chain-escape":
				snapshot.Symlinks = map[string]string{"a": ".", "b": "a/../outside"}
			case "link-parent":
				snapshot.Symlinks["tools"] = "."
			case "missing-mode":
				delete(snapshot.FileModes, "tools/run.sh")
			case "invalid-mode":
				snapshot.FileModes["tools/run.sh"] = 04000
			}
			snapshots := []recipe.FunctionPackageSnapshot{snapshot}
			if mode == "duplicate" {
				snapshots = append(snapshots, snapshot)
			}
			if mode == "missing" {
				snapshots = []recipe.FunctionPackageSnapshot{}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			restored, err := capturedPackage(ctx, "selector", snapshots)
			if mode != "valid" && mode != "valid-aliased-temp" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			file := filepath.Join(restored.ProjectRoot, "tools/run.sh")
			info, err := os.Stat(file)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0755), info.Mode().Perm())
			linked, err := os.ReadFile(filepath.Join(restored.ProjectRoot, "tools/run-link"))
			require.NoError(t, err)
			require.Contains(t, string(linked), "captured")
		})
	}
}

func TestCaptureLinkInsideAliasedRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "source")
	require.NoError(t, os.Mkdir(root, 0700))
	alias := filepath.Join(parent, "alias")
	require.NoError(t, os.Symlink(root, alias))
	require.NoError(t, os.WriteFile(filepath.Join(root, "file"), []byte("inside"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(parent, "outside"), []byte("outside"), 0600))
	require.NoError(t, os.Symlink("file", filepath.Join(root, "link")))
	require.NoError(t, os.Symlink("link", filepath.Join(root, "chain")))
	require.NoError(t, os.Symlink(".", filepath.Join(root, "self")))
	// Lexically within the source, but resolving self before .. escapes it.
	require.NoError(t, os.Symlink("self/../outside", filepath.Join(root, "escape")))
	for _, source := range []string{root, alias} {
		t.Run(filepath.Base(source), func(t *testing.T) {
			require.NoError(t, captureLinkInside(source, "link"))
			require.NoError(t, captureLinkInside(source, "chain"))
			require.NoError(t, captureLinkInside(source, "self"))
			require.ErrorContains(t, captureLinkInside(source, "escape"), "escapes source")
		})
	}
}
