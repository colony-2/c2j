package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func dependentFixture(t *testing.T, raw string) (string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, configDirName, configFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(raw), 0o640); err != nil {
		t.Fatal(err)
	}
	return root, path
}

func readDependentFixture(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDependentEntries(t *testing.T) {
	root, _ := dependentFixture(t, `base: go
pattern: 'github.com/acme/${{ cell }}'
dependents:
  - github.com/other/external
  - repo: github.com/acme/auth
    description: |
      Identity.
      Sessions.
  - repo: github.com/other/external
    description: External service
`)
	cfg, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := cfg.AllowedDependentEntries(context.Background())
	want := []DependentEntry{{"github.com/other/external", "External service"}, {"github.com/acme/auth", "Identity.\nSessions.\n"}}
	if err != nil || !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
	for _, project := range []func(context.Context) ([]string, error){cfg.DependentRepos, cfg.AllowedDependentRepos, cfg.DependentCells, cfg.AllowedCells} {
		repos, err := project(context.Background())
		if err != nil || !reflect.DeepEqual(repos, []string{want[0].Repo, want[1].Repo}) {
			t.Fatalf("repo projection = %v, %v", repos, err)
		}
	}
}

func TestDependentEntryValidation(t *testing.T) {
	for _, tc := range []struct{ yaml, error string }{
		{"- {description: text}", "repo must not be empty"},
		{"- {repo: auth}", "is not a repo"},
		{"- {repo: github.com/acme/auth, description: 42}", "description must be a string"},
		{"- {repo: [github.com/acme/auth]}", "repo must be a string"},
		{"- {repo: github.com/acme/auth, extra: text}", "unknown field"},
		{"- {repo: github.com/acme/auth, repo: github.com/acme/billing}", "duplicate field"},
		{"- true", "expected a repository string"},
		{"- {repo: github.com/acme/auth, description: one}\n  - {repo: github.com/acme/auth, description: two}", "conflicting descriptions"},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			root, _ := dependentFixture(t, "dependents:\n  "+tc.yaml+"\n")
			_, err := LoadProjectConfig(root)
			if err == nil || !strings.Contains(err.Error(), tc.error) {
				t.Fatalf("error = %v, want %s", err, tc.error)
			}
		})
	}
}

func TestDependentEditRoundTrip(t *testing.T) {
	original := `# Project config
pattern: 'github.com/acme/${{ cell }}'
custom: # Keep unknown config
  value: 'unchanged'
dependents:
  # Auth comment
  - github.com/acme/auth # Used by API
  - github.com/acme/billing
self:
  repo:
    command: exit 99
`
	root, path := dependentFixture(t, original)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	description := "Identity.\nSessions.\n"
	edit, err := PrepareDependentEdit(context.Background(), nested, "auth", &description)
	if err != nil {
		t.Fatal(err)
	}
	if edit.Path != path || edit.Status != "updated" || !edit.Changed {
		t.Fatalf("unexpected edit: %#v", edit)
	}
	if readDependentFixture(t, path) != original {
		t.Fatal("preparing edit changed file")
	}
	if err := edit.Write(); err != nil {
		t.Fatal(err)
	}
	written := readDependentFixture(t, path)
	for _, text := range []string{"# Project config", "# Keep unknown config", "'unchanged'", "# Auth comment", "# Used by API", "command: exit 99"} {
		if !strings.Contains(written, text) {
			t.Fatalf("missing %q in %s", text, written)
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("permissions = %v", info.Mode())
	}
	cfg, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := cfg.DependentEntries(context.Background())
	if err != nil || entries[0].Description != description || len(entries) != 2 {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
	for _, desc := range []*string{nil, &description} {
		repeat, err := PrepareDependentEdit(context.Background(), root, "https://github.com/acme/auth.git", desc)
		if err != nil || repeat.Changed || repeat.Repo != "github.com/acme/auth" {
			t.Fatalf("repeat = %#v, %v", repeat, err)
		}
		if err := repeat.Write(); err != nil {
			t.Fatal(err)
		}
		if readDependentFixture(t, path) != written {
			t.Fatal("no-op changed file bytes")
		}
	}
	empty := ""
	clear, err := PrepareDependentEdit(context.Background(), root, "auth", &empty)
	if err != nil {
		t.Fatal(err)
	}
	if err := clear.Write(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readDependentFixture(t, path), "description:") {
		t.Fatal("description was not cleared")
	}
	added, err := PrepareDependentEdit(context.Background(), root, "shared", nil)
	if err != nil {
		t.Fatal(err)
	}
	if added.Status != "added" {
		t.Fatalf("status = %s", added.Status)
	}
	if err := added.Write(); err != nil {
		t.Fatal(err)
	}
	cfg, _ = LoadProjectConfig(root)
	entries, _ = cfg.DependentEntries(context.Background())
	if len(entries) != 3 || entries[2].Repo != "github.com/acme/shared" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestDependentEditEmptyConfig(t *testing.T) {
	for _, raw := range []string{"", "# Only a comment\n", "dependents: []\n", "self: {repo: github.com/acme/self}\n"} {
		t.Run(raw, func(t *testing.T) {
			root, _ := dependentFixture(t, raw)
			edit, err := PrepareDependentEdit(context.Background(), root, "github.com/acme/auth", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := edit.Write(); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, "# Only") && !strings.Contains(string(edit.YAML), "# Only a comment") {
				t.Fatal("lost document comment")
			}
		})
	}
}

func TestDependentEditRejectedWithoutChanges(t *testing.T) {
	for _, tc := range []struct{ raw, error string }{
		{"dependents: [", "yaml:"},
		{"dependents: []\n---\ndependents: []\n", "multiple YAML documents"},
		{"items: &items []\ndependents: *items\n", "aliases"},
		{"defaults: &defaults {dependents: []}\n<<: *defaults\n", "merge keys"},
		{"dependents: [&auth github.com/acme/auth]\ncustom: *auth\n", "anchors"},
		{"dependents: [github.com/acme/auth, 'https://github.com/acme/auth.git']\n", "multiple entries match"},
		{"dependents: {filter: acme}\n", "sequence form"},
	} {
		t.Run(tc.error, func(t *testing.T) {
			root, path := dependentFixture(t, tc.raw)
			_, err := PrepareDependentEdit(context.Background(), root, "github.com/acme/auth", nil)
			if err == nil || !strings.Contains(err.Error(), tc.error) {
				t.Fatalf("error = %v, want %s", err, tc.error)
			}
			if readDependentFixture(t, path) != tc.raw {
				t.Fatal("rejected edit changed config")
			}
		})
	}
}

func TestDependentEditDiscovery(t *testing.T) {
	raw := "dependents:\n  command: printf '%s\\n' github.com/acme/auth github.com/other/external\n  filter: '^github.com/acme/'\n"
	root, path := dependentFixture(t, raw)
	edit, err := PrepareDependentEdit(context.Background(), root, "github.com/acme/auth", nil)
	if err != nil || edit.Changed || edit.Status != "already detected" {
		t.Fatalf("detected edit = %#v, %v", edit, err)
	}
	desc := "Identity"
	for _, tc := range []struct {
		repo string
		desc *string
	}{{"github.com/acme/auth", &desc}, {"github.com/acme/new", nil}, {"github.com/other/external", nil}} {
		_, err := PrepareDependentEdit(context.Background(), root, tc.repo, tc.desc)
		if err == nil || !strings.Contains(err.Error(), "uses discovery") {
			t.Fatalf("discovery edit error = %v", err)
		}
	}
	if readDependentFixture(t, path) != raw {
		t.Fatal("discovery config was rewritten")
	}
	root, _ = dependentFixture(t, "dependents: {command: 'exit 7'}\n")
	if _, err := PrepareDependentEdit(context.Background(), root, "github.com/acme/auth", nil); err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("discovery failure = %v", err)
	}
}

func TestDependentEditWriteGuards(t *testing.T) {
	root, path := dependentFixture(t, "dependents: []\n")
	edit, err := PrepareDependentEdit(context.Background(), root, "github.com/acme/auth", nil)
	if err != nil {
		t.Fatal(err)
	}
	const concurrent = "# Concurrent change\ndependents: []\n"
	if err := os.WriteFile(path, []byte(concurrent), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := edit.Write(); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("concurrent write error = %v", err)
	}
	if readDependentFixture(t, path) != concurrent {
		t.Fatal("overwrote concurrent edit")
	}
	temps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".config-*.yaml"))
	if len(temps) != 0 {
		t.Fatalf("temporary files left: %v", temps)
	}
	target := filepath.Join(root, "target.yaml")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDependentEdit(context.Background(), root, "github.com/acme/auth", nil); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error = %v", err)
	}
	if _, err := PrepareDependentEdit(context.Background(), t.TempDir(), "github.com/acme/auth", nil); err == nil || !strings.Contains(err.Error(), "c2j init --no-skills") {
		t.Fatalf("missing config error = %v", err)
	}
}

func TestDependentEditGoDiscoveryStaysLive(t *testing.T) {
	root, path := dependentFixture(t, "base: go\n")
	module := "module github.com/acme/app\ngo 1.26\nrequire github.com/acme/auth v0.0.0\nreplace github.com/acme/auth => ./auth\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "auth", "go.mod"), []byte("module github.com/acme/auth\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit, err := PrepareDependentEdit(context.Background(), root, "auth", nil)
	if err != nil || edit.Changed || edit.Status != "already detected" {
		t.Fatalf("Go discovery edit = %#v, %v", edit, err)
	}
	if readDependentFixture(t, path) != "base: go\n" {
		t.Fatal("Go discovery was copied into config")
	}
}

func TestDependentEditWriteFailurePreservesOriginal(t *testing.T) {
	root, path := dependentFixture(t, "dependents: []\n")
	edit, err := PrepareDependentEdit(context.Background(), root, "github.com/acme/auth", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Make the destination unavailable after preparation, even when testing as root.
	moved := filepath.Join(root, "moved-config")
	if err := os.Rename(filepath.Dir(path), moved); err != nil {
		t.Fatal(err)
	}
	if err := edit.Write(); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	if readDependentFixture(t, filepath.Join(moved, configFileName)) != "dependents: []\n" {
		t.Fatal("failed write changed the original")
	}
}
