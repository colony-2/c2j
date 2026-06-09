package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/colony-2/c2j/cmd/c2j/internal/buildinfo"
)

func TestRootCommandExposesVersionSubcommand(t *testing.T) {
	root := newRootCmd(buildinfo.Info{
		Version:       "dev-abcdef1-dirty",
		Commit:        "abcdef1234567890",
		Date:          "2026-06-09T02:23:30Z",
		Modified:      true,
		ModifiedKnown: true,
	})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("version command failed: %v", err)
	}

	got := out.String()
	want := "c2j version dev-abcdef1-dirty\n"
	if got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestRootCommandDoesNotExposeVersionFlag(t *testing.T) {
	root := newRootCmd(buildinfo.Info{Version: "dev"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--version"})

	err := root.Execute()
	if err == nil {
		t.Fatal("--version succeeded, want unknown flag error")
	}
	if !strings.Contains(err.Error(), "unknown flag: --version") {
		t.Fatalf("unexpected error: %v", err)
	}
}
