package compiler

import (
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
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

func TestRootRecipeLookupPrefersRecipeSourceContext(t *testing.T) {
	t.Parallel()

	jobContext := contextual.JobContext{
		GitBase: contextual.GitBaseContext{
			BaseRepo: "https://github.com/acme/self.git",
			BaseRef:  "main",
		},
		RecipeSource: contextual.RecipeSourceContext{
			Repo: "https://github.com/acme/templates.git",
			Ref:  "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		},
	}

	if got := rootRecipeLookupRepo(jobContext); got != "https://github.com/acme/templates.git" {
		t.Fatalf("rootRecipeLookupRepo() = %q", got)
	}
	if got := rootRecipeLookupRef(jobContext); got != "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" {
		t.Fatalf("rootRecipeLookupRef() = %q", got)
	}
}

func TestSubmittedArtifactRefsExcludesEmbeddedRecipeArtifact(t *testing.T) {
	t.Parallel()

	recipeArtifact := jobdb.NewArtifactFromBytes("review_docs.recipe.yaml", []byte("id: review_docs\n"))
	jobdb.AssignArtifactKey(recipeArtifact, jobdb.ArtifactKey{
		JobId:       "job-1",
		TaskOrdinal: 0,
		Name:        "review_docs.recipe.yaml",
		SizeBytes:   int64(len("id: review_docs\n")),
	})
	briefArtifact := jobdb.NewArtifactFromBytes("brief.md", []byte("brief"))
	jobdb.AssignArtifactKey(briefArtifact, jobdb.ArtifactKey{
		JobId:       "job-1",
		TaskOrdinal: 0,
		Name:        "brief.md",
		SizeBytes:   int64(len("brief")),
	})

	refs, err := submittedArtifactRefs(workflowctl.StartJob{
		RecipeName: "review_docs",
	}, []jobdb.Artifact{recipeArtifact, briefArtifact}, true)
	if err != nil {
		t.Fatalf("submittedArtifactRefs(): %v", err)
	}
	if _, exists := refs["review_docs.recipe.yaml"]; exists {
		t.Fatalf("internal recipe artifact was exposed: %#v", refs)
	}
	if got := refs["brief.md"].NameValue(); got != "brief.md" {
		t.Fatalf("brief ref name = %q", got)
	}
}

func TestSubmittedArtifactRefsRejectsDuplicateNames(t *testing.T) {
	t.Parallel()

	existing := recipeartifacts.NewStoredRef(jobdb.ArtifactKey{
		JobId:       "job-1",
		TaskOrdinal: 0,
		Name:        "brief.md",
		SizeBytes:   1,
	})
	briefArtifact := jobdb.NewArtifactFromBytes("brief.md", []byte("new"))
	jobdb.AssignArtifactKey(briefArtifact, jobdb.ArtifactKey{
		JobId:       "job-2",
		TaskOrdinal: 0,
		Name:        "brief.md",
		SizeBytes:   3,
	})

	_, err := submittedArtifactRefs(workflowctl.StartJob{
		RecipeName:   "review_docs",
		ArtifactRefs: []recipeartifacts.Ref{existing},
	}, []jobdb.Artifact{briefArtifact}, false)
	if err == nil {
		t.Fatal("expected duplicate submitted artifact name to fail")
	}
}
