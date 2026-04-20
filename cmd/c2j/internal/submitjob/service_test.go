package submitjob

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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
	}, targetCell{
		RepositorySource: "github.com/acme/demo",
		DefaultRef:       "main",
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
	}, targetCell{
		RepositorySource: "github.com/acme/demo",
		DefaultRef:       "main",
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
	want := "git+https://github.com/acme/demo.git//.c2j/recipes/deploy.yaml@main"
	if name != want {
		t.Fatalf("recipe name = %q, want %q", name, want)
	}
}

func TestLoadRecipeStartPassesThroughGitSelector(t *testing.T) {
	t.Parallel()

	selector := "git+https://github.com/acme/demo.git//.c2j/recipes/deploy.yaml@deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	name, rec, cleanup, err := loadRecipeStart(Options{
		Recipe:     selector,
		WorkingDir: t.TempDir(),
	}, targetCell{
		RepositorySource: "github.com/acme/demo",
		DefaultRef:       "main",
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
