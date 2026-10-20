package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/cmd/c2j/internal/buildinfo"
)

func TestCellAddDescriptionFlagPresence(t *testing.T) {
	rootDir := t.TempDir()
	t.Chdir(rootDir)
	if err := os.Mkdir(".c2", 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(rootDir, ".c2", "config.yaml")
	if err := os.WriteFile(path, []byte("dependents:\n  - repo: github.com/acme/auth\n    description: Original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		flags    []string
		contains bool
	}{
		{nil, true},
		{[]string{"--description", ""}, false},
		{[]string{"--description", "Original"}, true},
	} {
		root := newRootCmd(buildinfo.Info{})
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(append([]string{"cell", "add", "github.com/acme/auth", "--stdout"}, tc.flags...))
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "description: Original") != tc.contains {
			t.Fatalf("flags %v: output = %s", tc.flags, out.String())
		}
	}
}

func TestCellAddRequiresOneArgument(t *testing.T) {
	for _, args := range [][]string{{"cell", "add"}, {"cell", "add", "a", "b"}} {
		root := newRootCmd(buildinfo.Info{})
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(args)
		if err := root.Execute(); err == nil {
			t.Fatalf("args %v unexpectedly accepted", args)
		}
	}
}
