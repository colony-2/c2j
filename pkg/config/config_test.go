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
	if err := os.WriteFile(configPath, []byte("self:\n  repo: github.com/acme/self\n"), 0o644); err != nil {
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

	repo, err := cfg.SelfRepo(context.Background())
	if err != nil {
		t.Fatalf("SelfRepo(): %v", err)
	}
	if repo != "github.com/acme/self" {
		t.Fatalf("SelfRepo() = %q", repo)
	}
}

func TestDerivePatternFromRepo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		repo   string
		want   string
		wantOK bool
	}{
		{
			name:   "dash in last segment",
			repo:   "github.com/colony-2/foo-bar",
			want:   "github.com/colony-2/foo-" + cellPlaceholder,
			wantOK: true,
		},
		{
			name:   "slash fallback when last segment has no dash",
			repo:   "github.com/colony-2/foo/bar",
			want:   "github.com/colony-2/foo/" + cellPlaceholder,
			wantOK: true,
		},
		{
			name:   "empty repo",
			repo:   "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := derivePatternFromRepo(tt.repo)
			if ok != tt.wantOK {
				t.Fatalf("derivePatternFromRepo(%q) ok = %v, want %v", tt.repo, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("derivePatternFromRepo(%q) = %q, want %q", tt.repo, got, tt.want)
			}
		})
	}
}

func TestProjectConfig_ExplicitValuesAndPatternFiltering(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configYAML := `
pattern: 'github.com/acme/boo-${{ cell }}'
dependents:
  command: |
    printf '%s\n' \
      github.com/acme/boo-alpha \
      github.com/other/external \
      github.com/acme/boo-beta
self:
  repo: github.com/acme/boo-self
  ref:
    command: printf 'release\n'
root:
  repo: root
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}

	repo, err := cfg.SelfRepo(context.Background())
	if err != nil {
		t.Fatalf("SelfRepo(): %v", err)
	}
	if repo != "github.com/acme/boo-self" {
		t.Fatalf("SelfRepo() = %q", repo)
	}

	ref, err := cfg.SelfRef(context.Background())
	if err != nil {
		t.Fatalf("SelfRef(): %v", err)
	}
	if ref != "release" {
		t.Fatalf("SelfRef() = %q", ref)
	}

	rootRepo, err := cfg.RootRepo(context.Background())
	if err != nil {
		t.Fatalf("RootRepo(): %v", err)
	}
	if rootRepo != "github.com/acme/boo-root" {
		t.Fatalf("RootRepo() = %q", rootRepo)
	}

	rootRef, err := cfg.RootRef(context.Background())
	if err != nil {
		t.Fatalf("RootRef(): %v", err)
	}
	if rootRef != "release" {
		t.Fatalf("RootRef() = %q", rootRef)
	}

	repos, err := cfg.DependentRepos(context.Background())
	if err != nil {
		t.Fatalf("DependentRepos(): %v", err)
	}
	wantRepos := []string{
		"github.com/acme/boo-alpha",
		"github.com/other/external",
		"github.com/acme/boo-beta",
	}
	if !reflect.DeepEqual(repos, wantRepos) {
		t.Fatalf("DependentRepos() = %#v, want %#v", repos, wantRepos)
	}

	allowed, err := cfg.AllowedDependentRepos(context.Background())
	if err != nil {
		t.Fatalf("AllowedDependentRepos(): %v", err)
	}
	wantAllowed := []string{
		"github.com/acme/boo-alpha",
		"github.com/acme/boo-beta",
	}
	if !reflect.DeepEqual(allowed, wantAllowed) {
		t.Fatalf("AllowedDependentRepos() = %#v, want %#v", allowed, wantAllowed)
	}

	expanded, err := cfg.ExpandCellName(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("ExpandCellName(alpha): %v", err)
	}
	if expanded != "github.com/acme/boo-alpha" {
		t.Fatalf("ExpandCellName(alpha) = %q", expanded)
	}

	if name, ok := cfg.CellNameFromRepo(context.Background(), "https://github.com/acme/boo-beta.git"); !ok || name != "beta" {
		t.Fatalf("CellNameFromRepo(https url) = (%q, %v), want (beta, true)", name, ok)
	}
}

func TestProjectConfig_DependentsListIsExplicitFinalSet(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configYAML := `
base: go
pattern: 'github.com/acme/boo-${{ cell }}'
dependents:
  - github.com/other/external
  - github.com/acme/boo-alpha
self:
  repo: github.com/acme/boo-self
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	repos, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	} else {
		got, err := repos.AllowedDependentRepos(context.Background())
		if err != nil {
			t.Fatalf("AllowedDependentRepos(): %v", err)
		}
		want := []string{
			"github.com/other/external",
			"github.com/acme/boo-alpha",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("AllowedDependentRepos() = %#v, want %#v", got, want)
		}
	}
}

func TestProjectConfig_GoBaseDefaults(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".c2j", "config.yaml"), []byte("base: go\n"))

	monkeyDir := filepath.Join(root, "deps", "monkey")
	zebraDir := filepath.Join(root, "deps", "zebra")
	externalDir := filepath.Join(root, "deps", "external")

	mustWriteFile(t, filepath.Join(monkeyDir, "go.mod"), []byte("module github.com/acme/boo-monkey\ngo 1.26\n"))
	mustWriteFile(t, filepath.Join(zebraDir, "go.mod"), []byte("module github.com/acme/boo-zebra/v2\ngo 1.26\n"))
	mustWriteFile(t, filepath.Join(externalDir, "go.mod"), []byte("module github.com/other/external\ngo 1.26\n"))

	mainGoMod := `module github.com/acme/boo-cheetah

go 1.26

require (
	github.com/acme/boo-monkey v0.0.0
	github.com/acme/boo-zebra/v2 v2.0.0
	github.com/other/external v1.0.0
)

replace github.com/acme/boo-monkey => ./deps/monkey
replace github.com/acme/boo-zebra/v2 => ./deps/zebra
replace github.com/other/external => ./deps/external
`
	mustWriteFile(t, filepath.Join(root, "go.mod"), []byte(mainGoMod))

	cfg, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}

	repo, err := cfg.SelfRepo(context.Background())
	if err != nil {
		t.Fatalf("SelfRepo(): %v", err)
	}
	if repo != "github.com/acme/boo-cheetah" {
		t.Fatalf("SelfRepo() = %q", repo)
	}

	ref, err := cfg.SelfRef(context.Background())
	if err != nil {
		t.Fatalf("SelfRef(): %v", err)
	}
	if ref != "main" {
		t.Fatalf("SelfRef() = %q", ref)
	}

	rootRepo, err := cfg.RootRepo(context.Background())
	if err != nil {
		t.Fatalf("RootRepo(): %v", err)
	}
	if rootRepo != "github.com/acme/boo-root" {
		t.Fatalf("RootRepo() = %q", rootRepo)
	}

	rootRef, err := cfg.RootRef(context.Background())
	if err != nil {
		t.Fatalf("RootRef(): %v", err)
	}
	if rootRef != "main" {
		t.Fatalf("RootRef() = %q", rootRef)
	}

	repos, err := cfg.DependentRepos(context.Background())
	if err != nil {
		t.Fatalf("DependentRepos(): %v", err)
	}
	wantRepos := []string{
		"github.com/acme/boo-monkey",
		"github.com/acme/boo-zebra",
		"github.com/other/external",
	}
	if !reflect.DeepEqual(repos, wantRepos) {
		t.Fatalf("DependentRepos() = %#v, want %#v", repos, wantRepos)
	}

	allowed, err := cfg.AllowedDependentRepos(context.Background())
	if err != nil {
		t.Fatalf("AllowedDependentRepos(): %v", err)
	}
	wantAllowed := []string{
		"github.com/acme/boo-monkey",
		"github.com/acme/boo-zebra",
	}
	if !reflect.DeepEqual(allowed, wantAllowed) {
		t.Fatalf("AllowedDependentRepos() = %#v, want %#v", allowed, wantAllowed)
	}

	expanded, err := cfg.ExpandCellName(context.Background(), "monkey")
	if err != nil {
		t.Fatalf("ExpandCellName(monkey): %v", err)
	}
	if expanded != "github.com/acme/boo-monkey" {
		t.Fatalf("ExpandCellName(monkey) = %q", expanded)
	}
}

func TestProjectConfig_GoBaseDefaultsUseSlashBoundaryWhenRepoHasNoDashAfterSlash(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".c2j", "config.yaml"), []byte("base: go\n"))

	monkeyDir := filepath.Join(root, "deps", "monkey")
	externalDir := filepath.Join(root, "deps", "external")

	mustWriteFile(t, filepath.Join(monkeyDir, "go.mod"), []byte("module github.com/colony-2/foo/monkey\ngo 1.26\n"))
	mustWriteFile(t, filepath.Join(externalDir, "go.mod"), []byte("module github.com/other/external\ngo 1.26\n"))

	mainGoMod := `module github.com/colony-2/foo/bar

go 1.26

require (
	github.com/colony-2/foo/monkey v0.0.0
	github.com/other/external v1.0.0
)

replace github.com/colony-2/foo/monkey => ./deps/monkey
replace github.com/other/external => ./deps/external
`
	mustWriteFile(t, filepath.Join(root, "go.mod"), []byte(mainGoMod))

	cfg, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}

	pattern, err := cfg.Pattern(context.Background())
	if err != nil {
		t.Fatalf("Pattern(): %v", err)
	}
	if pattern != "github.com/colony-2/foo/"+cellPlaceholder {
		t.Fatalf("Pattern() = %q", pattern)
	}

	rootRepo, err := cfg.RootRepo(context.Background())
	if err != nil {
		t.Fatalf("RootRepo(): %v", err)
	}
	if rootRepo != "github.com/colony-2/foo/root" {
		t.Fatalf("RootRepo() = %q", rootRepo)
	}

	allowed, err := cfg.AllowedDependentRepos(context.Background())
	if err != nil {
		t.Fatalf("AllowedDependentRepos(): %v", err)
	}
	wantAllowed := []string{"github.com/colony-2/foo/monkey"}
	if !reflect.DeepEqual(allowed, wantAllowed) {
		t.Fatalf("AllowedDependentRepos() = %#v, want %#v", allowed, wantAllowed)
	}

	expanded, err := cfg.ExpandCellName(context.Background(), "monkey")
	if err != nil {
		t.Fatalf("ExpandCellName(monkey): %v", err)
	}
	if expanded != "github.com/colony-2/foo/monkey" {
		t.Fatalf("ExpandCellName(monkey) = %q", expanded)
	}
}

func TestProjectConfig_DependentsFilterOnlyUsesBaseDefaults(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".c2j", "config.yaml"), []byte(`
base: go
dependents:
  filter: '^github\.com/acme/boo-([A-Za-z0-9._-]+)$'
`))

	monkeyDir := filepath.Join(root, "deps", "monkey")
	externalDir := filepath.Join(root, "deps", "external")

	mustWriteFile(t, filepath.Join(monkeyDir, "go.mod"), []byte("module github.com/acme/boo-monkey\ngo 1.26\n"))
	mustWriteFile(t, filepath.Join(externalDir, "go.mod"), []byte("module github.com/other/external\ngo 1.26\n"))

	mainGoMod := `module github.com/acme/boo-cheetah

go 1.26

require (
	github.com/acme/boo-monkey v0.0.0
	github.com/other/external v1.0.0
)

replace github.com/acme/boo-monkey => ./deps/monkey
replace github.com/other/external => ./deps/external
`
	mustWriteFile(t, filepath.Join(root, "go.mod"), []byte(mainGoMod))

	cfg, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}

	got, err := cfg.AllowedDependentRepos(context.Background())
	if err != nil {
		t.Fatalf("AllowedDependentRepos(): %v", err)
	}
	want := []string{"github.com/acme/boo-monkey"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AllowedDependentRepos() = %#v, want %#v", got, want)
	}
}

func TestLoadProjectConfig_AutoDetectsGoBaseWithoutConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	nested := filepath.Join(root, "pkg", "child")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}

	mainGoMod := `module github.com/acme/boo-cheetah

go 1.26
`
	mustWriteFile(t, filepath.Join(root, "go.mod"), []byte(mainGoMod))

	cfg, err := LoadProjectConfig(nested)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}
	if cfg.Path() != "" {
		t.Fatalf("Path() = %q, want empty for auto-detected config", cfg.Path())
	}
	if cfg.RootDir() != root {
		t.Fatalf("RootDir() = %q, want %q", cfg.RootDir(), root)
	}

	repo, err := cfg.SelfRepo(context.Background())
	if err != nil {
		t.Fatalf("SelfRepo(): %v", err)
	}
	if repo != "github.com/acme/boo-cheetah" {
		t.Fatalf("SelfRepo() = %q", repo)
	}

	rootRepo, err := cfg.RootRepo(context.Background())
	if err != nil {
		t.Fatalf("RootRepo(): %v", err)
	}
	if rootRepo != "github.com/acme/boo-root" {
		t.Fatalf("RootRepo() = %q", rootRepo)
	}
}

func TestLoadProjectConfig_PrefersExplicitConfigOverAutoDetect(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "go.mod"), []byte("module github.com/acme/boo-cheetah\ngo 1.26\n"))
	mustWriteFile(t, filepath.Join(root, ".c2j", "config.yaml"), []byte(`
self:
  repo: github.com/acme/manual
  ref: release
`))

	cfg, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}
	if cfg.Path() == "" {
		t.Fatal("expected explicit config path to be set")
	}

	repo, err := cfg.SelfRepo(context.Background())
	if err != nil {
		t.Fatalf("SelfRepo(): %v", err)
	}
	if repo != "github.com/acme/manual" {
		t.Fatalf("SelfRepo() = %q", repo)
	}

	ref, err := cfg.SelfRef(context.Background())
	if err != nil {
		t.Fatalf("SelfRef(): %v", err)
	}
	if ref != "release" {
		t.Fatalf("SelfRef() = %q", ref)
	}
}

func TestProjectConfig_RootWithoutPatternCanStillResolveRootTicket(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".c2j", "config.yaml"), []byte(`
self:
  repo: github.com/acme/self
root:
  repo: github.com/acme/root
  ref: release
`))

	cfg, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}

	repo, err := cfg.ExpandCellName(context.Background(), "root")
	if err != nil {
		t.Fatalf("ExpandCellName(root): %v", err)
	}
	if repo != "github.com/acme/root" {
		t.Fatalf("ExpandCellName(root) = %q", repo)
	}

	if _, err := cfg.ExpandCellName(context.Background(), "monkey"); err == nil {
		t.Fatal("expected short name to fail without pattern")
	}
}

func TestProjectConfig_RejectsShortNameWithoutPattern(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".c2j", "config.yaml"), []byte(`
self:
  repo: cheetah
`))

	cfg, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}

	if _, err := cfg.SelfRepo(context.Background()); err == nil {
		t.Fatal("expected SelfRepo to reject a short name without pattern")
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
