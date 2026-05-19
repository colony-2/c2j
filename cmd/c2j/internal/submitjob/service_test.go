package submitjob

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResolveSelfTargetRejectsEmptySelfRepo(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("self:\n  ref: main\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := resolveSelfTarget(context.Background(), root)
	if err == nil {
		t.Fatal("expected resolveSelfTarget to reject an empty self.repo")
	}
}

func TestResolveSelfTargetUsesPatternShortName(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configYAML := `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: cheetah
  ref: release
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	target, err := resolveSelfTarget(context.Background(), root)
	if err != nil {
		t.Fatalf("resolveSelfTarget(): %v", err)
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
}

func TestResolveSelfTargetAutoDetectsGoBaseWithoutConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/acme/boo-cheetah\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	target, err := resolveSelfTarget(context.Background(), root)
	if err != nil {
		t.Fatalf("resolveSelfTarget(): %v", err)
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

func TestResolveExplicitTargetExpandsShortNameFromConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configYAML := `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: cheetah
  ref: release
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	target, err := resolveExplicitTarget(context.Background(), root, "monkey")
	if err != nil {
		t.Fatalf("resolveExplicitTarget(): %v", err)
	}
	if target.RepositorySource != "https://github.com/acme/boo-monkey.git" {
		t.Fatalf("RepositorySource = %q", target.RepositorySource)
	}
	if target.DefaultRef != "main" {
		t.Fatalf("DefaultRef = %q", target.DefaultRef)
	}
	if target.CellName != "monkey" {
		t.Fatalf("CellName = %q", target.CellName)
	}
}

func TestResolveExplicitTargetUsesRootConfigWithoutPattern(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configYAML := `
self:
  repo: github.com/acme/self
root:
  repo: github.com/acme/root
  ref: release
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	target, err := resolveExplicitTarget(context.Background(), root, "root")
	if err != nil {
		t.Fatalf("resolveExplicitTarget(root): %v", err)
	}
	if target.RepositorySource != "https://github.com/acme/root.git" {
		t.Fatalf("RepositorySource = %q", target.RepositorySource)
	}
	if target.DefaultRef != "release" {
		t.Fatalf("DefaultRef = %q", target.DefaultRef)
	}
	if target.CellName != "root" {
		t.Fatalf("CellName = %q", target.CellName)
	}

	target, err = resolveExplicitTarget(context.Background(), root, "https://github.com/acme/root.git")
	if err != nil {
		t.Fatalf("resolveExplicitTarget(root url): %v", err)
	}
	if target.DefaultRef != "release" {
		t.Fatalf("DefaultRef(url) = %q", target.DefaultRef)
	}
	if target.CellName != "root" {
		t.Fatalf("CellName(url) = %q", target.CellName)
	}
}

func TestLoadRecipeStartTreatsRecipeFlagLocalFileAsEmbeddedRecipe(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	recipePath := filepath.Join(root, "local-recipe.yaml")
	if err := os.WriteFile(recipePath, []byte("id: local_recipe\nversion: \"1.0\"\nsequence: []\n"), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}

	name, rec, cleanup, err := loadRecipeStart(Options{
		Recipe:     "local-recipe.yaml",
		WorkingDir: root,
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("loadRecipeStart: %v", err)
	}
	if name != "local_recipe" {
		t.Fatalf("recipe name = %q, want local_recipe", name)
	}
	if rec == nil {
		t.Fatal("expected embedded recipe for local file reference")
	}
}

func TestLoadRecipeStartTreatsBareRecipeAsCellSelector(t *testing.T) {
	t.Parallel()

	name, rec, cleanup, err := loadRecipeStart(Options{
		Recipe:     "deploy",
		WorkingDir: t.TempDir(),
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("loadRecipeStart: %v", err)
	}
	if rec != nil {
		t.Fatal("expected bare recipe reference to stay unresolved until execution")
	}
	if name != "deploy" {
		t.Fatalf("recipe name = %q, want %q", name, "deploy")
	}
}

func TestLoadRecipeStartPassesThroughGitSelector(t *testing.T) {
	t.Parallel()

	selector := "git+https://github.com/acme/demo.git//.c2j/recipes/deploy.yaml@deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	name, rec, cleanup, err := loadRecipeStart(Options{
		Recipe:     selector,
		WorkingDir: t.TempDir(),
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("loadRecipeStart: %v", err)
	}
	if rec != nil {
		t.Fatal("expected git selector to stay unresolved until execution")
	}
	if name != selector {
		t.Fatalf("recipe name = %q, want %q", name, selector)
	}
}

func TestLoadInputsAddsPromptField(t *testing.T) {
	t.Parallel()

	inputs, err := loadInputs(Options{
		Prompt:    "ship it",
		PromptSet: true,
	})
	if err != nil {
		t.Fatalf("loadInputs(): %v", err)
	}

	want := map[string]interface{}{"prompt": "ship it"}
	if !reflect.DeepEqual(inputs, want) {
		t.Fatalf("loadInputs() = %#v, want %#v", inputs, want)
	}
}

func TestLoadInputsMergesPromptWithExistingObject(t *testing.T) {
	t.Parallel()

	inputs, err := loadInputs(Options{
		InputsJSON: `{"topic":"infra"}`,
		Prompt:     "ship it",
		PromptSet:  true,
	})
	if err != nil {
		t.Fatalf("loadInputs(): %v", err)
	}

	want := map[string]interface{}{
		"topic":  "infra",
		"prompt": "ship it",
	}
	if !reflect.DeepEqual(inputs, want) {
		t.Fatalf("loadInputs() = %#v, want %#v", inputs, want)
	}
}

func TestLoadInputsRejectsPromptConflict(t *testing.T) {
	t.Parallel()

	if _, err := loadInputs(Options{
		InputsJSON: `{"prompt":"from-json"}`,
		Prompt:     "from-arg",
		PromptSet:  true,
	}); err == nil {
		t.Fatal("expected loadInputs() to reject duplicate prompt sources")
	}
}

func TestLoadSubmitArtifactsLoadsDefaultAndCustomNames(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docsDir := filepath.Join(root, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	briefPath := filepath.Join(docsDir, "brief.md")
	requirementsPath := filepath.Join(docsDir, "requirements.md")
	if err := os.WriteFile(briefPath, []byte("brief body"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := os.WriteFile(requirementsPath, []byte("requirements body"), 0o644); err != nil {
		t.Fatalf("write requirements: %v", err)
	}

	artifacts, err := loadSubmitArtifacts(Options{
		WorkingDir: root,
		ArtifactSpecs: []string{
			"docs/brief.md",
			"requirements=docs/requirements.md",
		},
	}, "review_docs", false)
	if err != nil {
		t.Fatalf("loadSubmitArtifacts(): %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("len(artifacts) = %d, want 2", len(artifacts))
	}
	if artifacts[0].Name() != "brief.md" {
		t.Fatalf("first artifact name = %q", artifacts[0].Name())
	}
	if artifacts[1].Name() != "requirements" {
		t.Fatalf("second artifact name = %q", artifacts[1].Name())
	}

	firstBytes, err := artifacts[0].Bytes(context.Background())
	if err != nil {
		t.Fatalf("read first artifact: %v", err)
	}
	if string(firstBytes) != "brief body" {
		t.Fatalf("first artifact bytes = %q", firstBytes)
	}
	secondBytes, err := artifacts[1].Bytes(context.Background())
	if err != nil {
		t.Fatalf("read second artifact: %v", err)
	}
	if string(secondBytes) != "requirements body" {
		t.Fatalf("second artifact bytes = %q", secondBytes)
	}
}

func TestLoadSubmitArtifactsRejectsInvalidSpecs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "brief.md"), []byte("brief"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}

	tests := []struct {
		name           string
		specs          []string
		embeddedRecipe bool
		want           string
	}{
		{name: "empty", specs: []string{""}, want: "cannot be empty"},
		{name: "missing file", specs: []string{"missing.md"}, want: "stat"},
		{name: "directory", specs: []string{"docs"}, want: "regular file"},
		{name: "invalid name", specs: []string{"../bad=brief.md"}, want: "invalid artifact name"},
		{name: "duplicate name", specs: []string{"brief.md", "brief.md=brief.md"}, want: "duplicates artifact name"},
		{name: "recipe artifact collision", specs: []string{"review_docs.recipe.yaml=brief.md"}, embeddedRecipe: true, want: "conflicts with internal recipe artifact"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadSubmitArtifacts(Options{
				WorkingDir:    root,
				ArtifactSpecs: tt.specs,
			}, "review_docs", tt.embeddedRecipe)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}
