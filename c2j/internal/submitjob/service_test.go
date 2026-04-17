package submitjob

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSelfTargetRejectsEmptyCanonicalRepo(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("default_ref:\n  value: main\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := resolveSelfTarget(context.Background(), root)
	if err == nil {
		t.Fatal("expected resolveSelfTarget to reject an empty canonical_repo")
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
