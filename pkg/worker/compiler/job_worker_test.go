package compiler

import (
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
)

func TestApplyRootRecipeSourceUsesResolvedGitSelector(t *testing.T) {
	t.Parallel()

	runContext := contextual.JobContext{}
	applyRootRecipeSource(&runContext, RecipeSourceResolution{
		SourceKind:       RecipeSourceKindGit,
		ResolvedSelector: "git+https://github.com/acme/templates.git//.c2j/recipes/default.yaml@deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	})

	if runContext.RecipeSource.Repo != "https://github.com/acme/templates.git" {
		t.Fatalf("RecipeSource.Repo = %q", runContext.RecipeSource.Repo)
	}
	if runContext.RecipeSource.Ref != "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" {
		t.Fatalf("RecipeSource.Ref = %q", runContext.RecipeSource.Ref)
	}
	if runContext.RecipeSource.Path != ".c2j/recipes/default.yaml" {
		t.Fatalf("RecipeSource.Path = %q", runContext.RecipeSource.Path)
	}
}
