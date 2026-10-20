package celledit

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/config"
)

func TestRunPreviewAndWrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".c2", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const original = "# Header\ndependents: []\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	description := "Auth service"
	opts := Options{WorkingDir: root, Cell: "github.com/acme/auth", Description: &description, StdoutOnly: true, Stdout: &out}
	if err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	preview := out.String()
	if !strings.HasPrefix(preview, "# Header") || !strings.Contains(preview, "description: Auth service") {
		t.Fatalf("preview = %s", preview)
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatal("preview wrote config")
	}
	for _, status := range []string{"added", "unchanged"} {
		out.Reset()
		opts.StdoutOnly = false
		if err := Run(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out.String(), status+" ") || !strings.Contains(out.String(), path) {
			t.Fatalf("status output = %s", out.String())
		}
	}
	data, _ = os.ReadFile(path)
	if string(data) != preview {
		t.Fatal("preview and write differ")
	}
	cfg, err := config.LoadProjectConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := cfg.AllowedDependentEntries(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Description != description {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
}
