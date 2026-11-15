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
	for _, mode := range []string{"valid", "path", "link-escape", "link-chain-escape", "link-parent", "missing-mode", "invalid-mode", "duplicate", "missing"} {
		t.Run(mode, func(t *testing.T) {
			snapshot := recipe.FunctionPackageSnapshot{Selector: "selector", ResolvedSelector: "pinned", PackageDir: "tools", Files: map[string]string{"tools/run.sh": base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nprintf captured"))}, FileModes: map[string]uint32{"tools/run.sh": 0755}, Symlinks: map[string]string{"tools/run-link": "run.sh"}}
			switch mode {
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
			if mode != "valid" {
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
