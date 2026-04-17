package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadProjectConfigDiscoversNearestConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("canonical_repo:\n  value: github.com/acme/self\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}

	cfg, err := LoadProjectConfig(nested)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}
	if cfg.Path() != configPath {
		t.Fatalf("Path() = %q, want %q", cfg.Path(), configPath)
	}
	if cfg.RootDir() != root {
		t.Fatalf("RootDir() = %q, want %q", cfg.RootDir(), root)
	}

	repo, err := cfg.CanonicalRepo(context.Background())
	if err != nil {
		t.Fatalf("CanonicalRepo(): %v", err)
	}
	if repo != "github.com/acme/self" {
		t.Fatalf("CanonicalRepo() = %q", repo)
	}
}

func TestProjectConfig_CommandValueAndFilter(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configYAML := `
cell_filter: '^github\.com/acme/[^/]+$'
cells:
  command: |
    printf '%s\n' \
      github.com/acme/alpha \
      github.com/other/external \
      github.com/acme/beta
root_cell:
  value: github.com/acme/root
canonical_repo:
  value: github.com/acme/self
default_ref:
  command: printf 'release\n'
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}

	cells, err := cfg.DependentCells(context.Background())
	if err != nil {
		t.Fatalf("DependentCells(): %v", err)
	}
	wantCells := []string{
		"github.com/acme/alpha",
		"github.com/other/external",
		"github.com/acme/beta",
	}
	if !reflect.DeepEqual(cells, wantCells) {
		t.Fatalf("DependentCells() = %#v, want %#v", cells, wantCells)
	}

	allowed, err := cfg.AllowedCells(context.Background())
	if err != nil {
		t.Fatalf("AllowedCells(): %v", err)
	}
	wantAllowed := []string{
		"github.com/acme/alpha",
		"github.com/acme/beta",
	}
	if !reflect.DeepEqual(allowed, wantAllowed) {
		t.Fatalf("AllowedCells() = %#v, want %#v", allowed, wantAllowed)
	}

	rootCell, err := cfg.RootCell(context.Background())
	if err != nil {
		t.Fatalf("RootCell(): %v", err)
	}
	if rootCell != "github.com/acme/root" {
		t.Fatalf("RootCell() = %q", rootCell)
	}

	repo, err := cfg.CanonicalRepo(context.Background())
	if err != nil {
		t.Fatalf("CanonicalRepo(): %v", err)
	}
	if repo != "github.com/acme/self" {
		t.Fatalf("CanonicalRepo() = %q", repo)
	}

	ref, err := cfg.DefaultRef(context.Background())
	if err != nil {
		t.Fatalf("DefaultRef(): %v", err)
	}
	if ref != "release" {
		t.Fatalf("DefaultRef() = %q", ref)
	}
}

func TestProjectConfig_GoParentDefaults(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	mustWriteFile(t, filepath.Join(root, ".c2j", "config.yaml"), []byte("parent: go\n"))

	libDir := filepath.Join(root, "deps", "lib")
	toolDir := filepath.Join(root, "deps", "toolv2")
	externalDir := filepath.Join(root, "deps", "external")

	mustWriteFile(t, filepath.Join(libDir, "go.mod"), []byte("module github.com/acme/lib\ngo 1.26\n"))
	mustWriteFile(t, filepath.Join(toolDir, "go.mod"), []byte("module github.com/acme/tool/v2\ngo 1.26\n"))
	mustWriteFile(t, filepath.Join(externalDir, "go.mod"), []byte("module github.com/other/external\ngo 1.26\n"))

	mainGoMod := `module github.com/acme/self

go 1.26

require (
	github.com/acme/lib v0.0.0
	github.com/acme/tool/v2 v2.0.0
	github.com/other/external v1.0.0
)

replace github.com/acme/lib => ./deps/lib
replace github.com/acme/tool/v2 => ./deps/toolv2
replace github.com/other/external => ./deps/external
`
	mustWriteFile(t, filepath.Join(root, "go.mod"), []byte(mainGoMod))

	cfg, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}

	repo, err := cfg.CanonicalRepo(context.Background())
	if err != nil {
		t.Fatalf("CanonicalRepo(): %v", err)
	}
	if repo != "github.com/acme/self" {
		t.Fatalf("CanonicalRepo() = %q", repo)
	}

	rootCell, err := cfg.RootCell(context.Background())
	if err != nil {
		t.Fatalf("RootCell(): %v", err)
	}
	if rootCell != "github.com/acme/self" {
		t.Fatalf("RootCell() = %q", rootCell)
	}

	ref, err := cfg.DefaultRef(context.Background())
	if err != nil {
		t.Fatalf("DefaultRef(): %v", err)
	}
	if ref != "main" {
		t.Fatalf("DefaultRef() = %q", ref)
	}

	cells, err := cfg.DependentCells(context.Background())
	if err != nil {
		t.Fatalf("DependentCells(): %v", err)
	}
	wantCells := []string{
		"github.com/acme/lib",
		"github.com/acme/tool",
		"github.com/other/external",
	}
	if !reflect.DeepEqual(cells, wantCells) {
		t.Fatalf("DependentCells() = %#v, want %#v", cells, wantCells)
	}

	allowed, err := cfg.AllowedCells(context.Background())
	if err != nil {
		t.Fatalf("AllowedCells(): %v", err)
	}
	wantAllowed := []string{
		"github.com/acme/lib",
		"github.com/acme/tool",
	}
	if !reflect.DeepEqual(allowed, wantAllowed) {
		t.Fatalf("AllowedCells() = %#v, want %#v", allowed, wantAllowed)
	}
}

func mustWriteFile(t *testing.T, path string, contents []byte) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
