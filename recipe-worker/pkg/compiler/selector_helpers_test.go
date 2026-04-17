package compiler

import (
	"path/filepath"
	"testing"
)

func TestNormalizeGitRepositorySource(t *testing.T) {
	t.Parallel()

	localRepo := t.TempDir()
	absLocalRepo, err := filepath.Abs(localRepo)
	if err != nil {
		t.Fatalf("Abs(%q): %v", localRepo, err)
	}

	tests := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "local path",
			source: localRepo,
			want:   "file://" + filepath.ToSlash(absLocalRepo),
		},
		{
			name:   "canonical repo ref",
			source: "github.com/acme/demo",
			want:   "https://github.com/acme/demo.git",
		},
		{
			name:   "https url",
			source: "https://github.com/acme/demo.git",
			want:   "https://github.com/acme/demo.git",
		},
		{
			name:   "ssh scp syntax",
			source: "git@github.com:acme/demo.git",
			want:   "ssh://git@github.com/acme/demo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeGitRepositorySource(tt.source)
			if err != nil {
				t.Fatalf("NormalizeGitRepositorySource(%q): %v", tt.source, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeGitRepositorySource(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

func TestBuildCellRecipeSelector(t *testing.T) {
	t.Parallel()

	selector, err := BuildCellRecipeSelector("github.com/acme/demo", "deploy", "")
	if err != nil {
		t.Fatalf("BuildCellRecipeSelector(): %v", err)
	}

	want := "git+https://github.com/acme/demo.git//.c2j/recipes/deploy.yaml@main"
	if selector != want {
		t.Fatalf("BuildCellRecipeSelector() = %q, want %q", selector, want)
	}
}

func TestIsLocalRecipeFileReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  bool
	}{
		{value: "deploy", want: false},
		{value: "deploy.yaml", want: true},
		{value: "recipes/deploy.yaml", want: true},
		{value: "./deploy.yaml", want: true},
		{value: "../deploy.yaml", want: true},
		{value: "/tmp/deploy.yaml", want: true},
		{value: "git+https://github.com/acme/demo.git//.c2j/recipes/deploy.yaml@main", want: false},
	}

	for _, tt := range tests {
		if got := IsLocalRecipeFileReference(tt.value); got != tt.want {
			t.Fatalf("IsLocalRecipeFileReference(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestIsCellRecipeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  bool
	}{
		{value: "deploy", want: true},
		{value: "deploy.yaml", want: false},
		{value: "./deploy.yaml", want: false},
		{value: "git+https://github.com/acme/demo.git//.c2j/recipes/deploy.yaml@main", want: false},
	}

	for _, tt := range tests {
		if got := IsCellRecipeName(tt.value); got != tt.want {
			t.Fatalf("IsCellRecipeName(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestRepositoryNameFromSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		source string
		want   string
	}{
		{source: "github.com/acme/demo", want: "demo"},
		{source: "https://github.com/acme/demo.git", want: "demo"},
		{source: "git@github.com:acme/demo.git", want: "demo"},
	}

	for _, tt := range tests {
		got := RepositoryNameFromSource(tt.source)
		if got != tt.want {
			t.Fatalf("RepositoryNameFromSource(%q) = %q, want %q", tt.source, got, tt.want)
		}
	}
}
