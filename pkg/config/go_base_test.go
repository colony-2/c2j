package config

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGoBaseWithoutToolchain(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	ctx := context.Background()
	for _, explicit := range []bool{false, true} {
		name := "auto"
		if explicit {
			name = "explicit"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if explicit {
				mustWriteFile(t, filepath.Join(root, ".c2", "config.yaml"), []byte("base: go\n"))
			}
			// No dependency manifests exist, and replacements must not be followed.
			mustWriteFile(t, filepath.Join(root, "go.mod"), []byte(`module github.com/acme/boo-self/v2

go 1.26

require (
	github.com/acme/boo-zebra/v2 v2.0.0
	github.com/acme/boo-indirect v1.0.0 // indirect
	github.com/acme/boo-alpha v1.0.0
	github.com/acme/boo-zebra v1.0.0
	github.com/other/external v1.0.0
)

replace github.com/acme/boo-alpha => ./missing
`))
			nested := filepath.Join(root, "pkg", "nested")
			if err := os.MkdirAll(nested, 0o755); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadProjectConfig(nested)
			if err != nil {
				t.Fatal(err)
			}
			repo, err := cfg.SelfRepo(ctx)
			if err != nil || repo != "github.com/acme/boo-self" {
				t.Fatalf("SelfRepo() = %q, %v", repo, err)
			}
			repos, err := cfg.DependentRepos(ctx)
			want := []string{"github.com/acme/boo-alpha", "github.com/acme/boo-zebra", "github.com/other/external"}
			if err != nil || !reflect.DeepEqual(repos, want) {
				t.Fatalf("DependentRepos() = %v, %v; want %v", repos, err, want)
			}
			allowed, err := cfg.AllowedDependentRepos(ctx)
			if err != nil || !reflect.DeepEqual(allowed, want[:2]) {
				t.Fatalf("AllowedDependentRepos() = %v, %v; want %v", allowed, err, want[:2])
			}
			template, err := RenderInitConfigTemplate(ctx, nested)
			if err != nil || !strings.Contains(string(template), "\nbase: go\n") || !strings.Contains(string(template), "# Derived current repo: github.com/acme/boo-self\n") {
				t.Fatalf("RenderInitConfigTemplate() = %s, %v", template, err)
			}
		})
	}
}

func TestGoBaseModuleErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, contents, want string
	}{
		{"missing", "", "find go.mod"},
		{"malformed", "module github.com/acme/self\nrequire (\n", "parse "},
		{"no module", "go 1.26\n", "missing module directive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			mustWriteFile(t, filepath.Join(root, ".c2", "config.yaml"), []byte("base: go\n"))
			if tc.contents != "" {
				mustWriteFile(t, filepath.Join(root, "go.mod"), []byte(tc.contents))
			}
			cfg, err := LoadProjectConfig(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.SelfRepo(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SelfRepo() error = %v, want %q", err, tc.want)
			}
			if _, err := cfg.DependentRepos(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("DependentRepos() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestGoCommandBaseIncludesIndirectDependencies(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOPROXY", "off")
	ctx := context.Background()
	root := t.TempDir()
	configPath := filepath.Join(root, ".c2", "config.yaml")
	mustWriteFile(t, configPath, []byte("base: gocmd\n"))
	mustWriteFile(t, filepath.Join(root, "go.mod"), []byte(`module github.com/acme/boo-self

go 1.26

require (
	github.com/acme/boo-direct v1.0.0
	github.com/acme/boo-indirect v1.0.0 // indirect
)

replace github.com/acme/boo-direct => ./direct
replace github.com/acme/boo-indirect => ./indirect
`))
	mustWriteFile(t, filepath.Join(root, "direct", "go.mod"), []byte("module github.com/acme/boo-direct\ngo 1.26\nrequire github.com/acme/boo-indirect v1.0.0\n"))
	mustWriteFile(t, filepath.Join(root, "indirect", "go.mod"), []byte("module github.com/acme/boo-indirect\ngo 1.26\n"))
	cfg, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	repos, err := cfg.AllowedDependentRepos(ctx)
	want := []string{"github.com/acme/boo-direct", "github.com/acme/boo-indirect"}
	if err != nil || !reflect.DeepEqual(repos, want) {
		t.Fatalf("AllowedDependentRepos() = %v, %v; want %v", repos, err, want)
	}
	edit, err := PrepareDependentEdit(ctx, root, "indirect", nil)
	if err != nil || edit.Status != "already detected" || edit.Changed {
		t.Fatalf("PrepareDependentEdit() = %+v, %v", edit, err)
	}
	if _, err := PrepareDependentEdit(ctx, root, "unknown", nil); err == nil || !strings.Contains(err.Error(), "uses discovery") {
		t.Fatalf("PrepareDependentEdit(unknown) error = %v", err)
	}
	// Switching the same project to go excludes indirect entries.
	mustWriteFile(t, configPath, []byte("base: go\n"))
	cfg, err = LoadProjectConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	repos, err = cfg.AllowedDependentRepos(ctx)
	if err != nil || !reflect.DeepEqual(repos, want[:1]) {
		t.Fatalf("AllowedDependentRepos() with base go = %v, %v", repos, err)
	}
}

func TestGoCommandBaseRequiresToolchain(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".c2", "config.yaml"), []byte("base: gocmd\n"))
	mustWriteFile(t, filepath.Join(root, "go.mod"), []byte("module github.com/acme/self\n"))
	cfg, err := LoadProjectConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.SelfRepo(context.Background()); err == nil || !strings.Contains(err.Error(), "go list") {
		t.Fatalf("SelfRepo() error = %v, want missing go command", err)
	}
}
