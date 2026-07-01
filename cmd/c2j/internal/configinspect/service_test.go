package configinspect

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCellsText(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
dependents:
  command: |
    printf '%s\n' \
      github.com/acme/boo-alpha \
      github.com/other/external \
      github.com/acme/boo-beta
self:
  repo: cheetah
  ref: main
`)

	var stdout bytes.Buffer
	if err := RunCells(context.Background(), CellsOptions{
		WorkingDir: root,
		Stdout:     &stdout,
	}); err != nil {
		t.Fatalf("RunCells(): %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "SHORT NAME") || !strings.Contains(out, "REPO") {
		t.Fatalf("expected header in output, got:\n%s", out)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "github.com/acme/boo-alpha") {
		t.Fatalf("expected alpha row, got:\n%s", out)
	}
	if strings.Contains(out, "github.com/other/external") {
		t.Fatalf("expected filter to exclude external repo, got:\n%s", out)
	}
}

func TestRunCellsJSON(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
dependents:
  command: |
    printf '%s\n' \
      github.com/acme/boo-alpha \
      github.com/acme/boo-beta
self:
  repo: cheetah
`)

	var stdout bytes.Buffer
	if err := RunCells(context.Background(), CellsOptions{
		WorkingDir: root,
		JSONOutput: true,
		Stdout:     &stdout,
	}); err != nil {
		t.Fatalf("RunCells(): %v", err)
	}

	var rows []CellInfo
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %#v", rows)
	}
	if rows[0].ShortName != "alpha" || rows[0].Repo != "github.com/acme/boo-alpha" {
		t.Fatalf("unexpected first row: %#v", rows[0])
	}
}

func TestRunSelfText(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: cheetah
  ref: release
jobdb: https://jobdb.example.com/acme-prod
root:
  repo: root
`)

	var stdout bytes.Buffer
	if err := RunSelf(context.Background(), SelfOptions{
		WorkingDir: root,
		Stdout:     &stdout,
	}); err != nil {
		t.Fatalf("RunSelf(): %v", err)
	}

	out := stdout.String()
	for _, want := range []string{"short_name", "cheetah", "repo", "github.com/acme/boo-cheetah", "jobdb", "https://jobdb.example.com/acme-prod", "root_repo", "github.com/acme/boo-root", "pattern"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output, got:\n%s", want, out)
		}
	}
}

func TestRunSelfJSON(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeConfig(t, root, `
self:
  repo: github.com/acme/self
  ref: main
jobdb: https://jobdb.example.com/acme-prod
root:
  repo: github.com/acme/root
  ref: release
`)

	var stdout bytes.Buffer
	if err := RunSelf(context.Background(), SelfOptions{
		WorkingDir: root,
		JSONOutput: true,
		Stdout:     &stdout,
	}); err != nil {
		t.Fatalf("RunSelf(): %v", err)
	}

	var info SelfInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if info.Repo != "github.com/acme/self" || info.JobDB != "https://jobdb.example.com/acme-prod" || info.RootRepo != "github.com/acme/root" || info.RootRef != "release" {
		t.Fatalf("unexpected self info: %#v", info)
	}
}

func TestRunSelfAutoDetectsGoBaseWithoutConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/acme/boo-cheetah\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	var stdout bytes.Buffer
	if err := RunSelf(context.Background(), SelfOptions{
		WorkingDir: root,
		JSONOutput: true,
		Stdout:     &stdout,
	}); err != nil {
		t.Fatalf("RunSelf(): %v", err)
	}

	var info SelfInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if info.Repo != "github.com/acme/boo-cheetah" || info.JobDB != "" || info.ShortName != "cheetah" || info.RootRepo != "github.com/acme/boo-root" {
		t.Fatalf("unexpected autodetected self info: %#v", info)
	}
}

func writeConfig(t *testing.T, root string, raw string) {
	t.Helper()

	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(strings.TrimSpace(raw)+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
