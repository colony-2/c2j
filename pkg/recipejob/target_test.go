package recipejob

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveTargetSelfUsesPatternShortName(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: cheetah
  ref: release
`)

	target, err := ResolveTarget(context.Background(), ResolveTargetRequest{
		WorkingDir: root,
		Self:       true,
		TenantID:   "tenant",
	})
	if err != nil {
		t.Fatalf("ResolveTarget(): %v", err)
	}

	if target.RepositorySource != "https://github.com/acme/boo-cheetah.git" {
		t.Fatalf("RepositorySource = %q", target.RepositorySource)
	}
	if target.DefaultRef != "release" {
		t.Fatalf("DefaultRef = %q", target.DefaultRef)
	}
	if target.CellName != "cheetah" {
		t.Fatalf("CellName = %q", target.CellName)
	}
	if target.Source != TargetSourceSelf {
		t.Fatalf("Source = %q", target.Source)
	}
	if target.TenantID != "tenant" {
		t.Fatalf("TenantID = %q", target.TenantID)
	}
}

func TestResolveTargetAutoDetectsGoBaseWithoutConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/acme/boo-cheetah\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	target, err := ResolveTarget(context.Background(), ResolveTargetRequest{WorkingDir: root})
	if err != nil {
		t.Fatalf("ResolveTarget(): %v", err)
	}
	if target.RepositorySource != "https://github.com/acme/boo-cheetah.git" {
		t.Fatalf("RepositorySource = %q", target.RepositorySource)
	}
	if target.DefaultRef != "main" {
		t.Fatalf("DefaultRef = %q", target.DefaultRef)
	}
	if target.CellName != "cheetah" {
		t.Fatalf("CellName = %q", target.CellName)
	}
}

func TestResolveTargetExplicitValues(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: cheetah
  ref: release
root:
  repo: github.com/acme/root
  ref: stable
`)

	tests := []struct {
		name       string
		cell       string
		wantRepo   string
		wantRef    string
		wantName   string
		wantSource TargetSource
	}{
		{
			name:       "short name",
			cell:       "monkey",
			wantRepo:   "https://github.com/acme/boo-monkey.git",
			wantRef:    "main",
			wantName:   "monkey",
			wantSource: TargetSourceConfig,
		},
		{
			name:       "root",
			cell:       "root",
			wantRepo:   "https://github.com/acme/root.git",
			wantRef:    "stable",
			wantName:   "root",
			wantSource: TargetSourceConfig,
		},
		{
			name:       "canonical repo",
			cell:       "github.com/acme/boo-lemur",
			wantRepo:   "https://github.com/acme/boo-lemur.git",
			wantRef:    "main",
			wantName:   "lemur",
			wantSource: TargetSourceRepository,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			target, err := ResolveTarget(context.Background(), ResolveTargetRequest{
				WorkingDir: root,
				Cell:       tt.cell,
			})
			if err != nil {
				t.Fatalf("ResolveTarget(): %v", err)
			}
			if target.RepositorySource != tt.wantRepo {
				t.Fatalf("RepositorySource = %q, want %q", target.RepositorySource, tt.wantRepo)
			}
			if target.DefaultRef != tt.wantRef {
				t.Fatalf("DefaultRef = %q, want %q", target.DefaultRef, tt.wantRef)
			}
			if target.CellName != tt.wantName {
				t.Fatalf("CellName = %q, want %q", target.CellName, tt.wantName)
			}
			if target.Source != tt.wantSource {
				t.Fatalf("Source = %q, want %q", target.Source, tt.wantSource)
			}
		})
	}
}

func TestResolveTargetLocalPathUsesWorkingDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repoPath := filepath.Join(root, "repo")
	if err := os.Mkdir(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	target, err := ResolveTarget(context.Background(), ResolveTargetRequest{
		WorkingDir: root,
		Cell:       "./repo",
	})
	if err != nil {
		t.Fatalf("ResolveTarget(): %v", err)
	}

	want := (&url.URL{Scheme: "file", Path: filepath.ToSlash(repoPath)}).String()
	if target.RepositorySource != want {
		t.Fatalf("RepositorySource = %q, want %q", target.RepositorySource, want)
	}
	if target.Source != TargetSourceLocalPath {
		t.Fatalf("Source = %q", target.Source)
	}
}

func TestResolveTargetRejectsRecipeSelectorAsRepository(t *testing.T) {
	t.Parallel()

	_, err := ResolveTarget(context.Background(), ResolveTargetRequest{
		WorkingDir: t.TempDir(),
		Cell:       "git+https://github.com/acme/demo.git//.c2j/recipes/deploy.yaml@main",
	})
	if err == nil || !strings.Contains(err.Error(), "not a recipe selector") {
		t.Fatalf("expected recipe selector error, got %v", err)
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
