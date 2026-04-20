package initconfig

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWritesConfigFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/acme/boo-cheetah\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), Options{
		WorkingDir: root,
		Stdout:     &stdout,
	}); err != nil {
		t.Fatalf("Run(): %v", err)
	}

	configPath := filepath.Join(root, ".c2j", "config.yaml")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "base: go") {
		t.Fatalf("expected generated config to contain base: go, got:\n%s", text)
	}
	if !strings.Contains(text, "github.com/acme/boo-${{ cell }}") {
		t.Fatalf("expected generated config to mention the derived pattern, got:\n%s", text)
	}
	if !strings.Contains(stdout.String(), configPath) {
		t.Fatalf("expected stdout to mention the written path, got %q", stdout.String())
	}
}

func TestRunWritesSlashDerivedPattern(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/colony-2/foo/bar\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	if err := Run(context.Background(), Options{WorkingDir: root}); err != nil {
		t.Fatalf("Run(): %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, ".c2j", "config.yaml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "github.com/colony-2/foo/${{ cell }}") {
		t.Fatalf("expected generated config to mention the slash-derived pattern, got:\n%s", text)
	}
}

func TestRunRejectsExistingConfigWithoutForce(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("base: go\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	err := Run(context.Background(), Options{WorkingDir: root})
	if err == nil {
		t.Fatal("expected Run() to reject an existing config")
	}
}

func TestRunStdoutDoesNotWriteConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	var stdout bytes.Buffer
	if err := Run(context.Background(), Options{
		WorkingDir: root,
		StdoutOnly: true,
		Stdout:     &stdout,
	}); err != nil {
		t.Fatalf("Run(): %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, ".c2j", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected stdout-only mode to avoid writing config, stat err=%v", err)
	}
	if !strings.Contains(stdout.String(), "# c2j project config") {
		t.Fatalf("expected stdout-only output to contain the template header, got:\n%s", stdout.String())
	}
}
