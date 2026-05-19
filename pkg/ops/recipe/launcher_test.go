package recipe

import (
	"context"
	"errors"
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/swf-go/pkg/swf"
)

func TestStartJobsEmpty(t *testing.T) {
	ctl := &fakeWorkflowControl{}
	_, err := startJobs(context.Background(), swf.JobKey{TenantId: "tenant", JobId: "parent-job"}, contextual.Invocation{}, ctl, "github.com/acme/demo", "main", nil, "")
	if err == nil {
		t.Fatal("expected error for empty recipes")
	}
}

func TestStartJobsSingle(t *testing.T) {
	ctl := &fakeWorkflowControl{}
	recipes := []SingleRecipe{{
		Name:   "child",
		Inputs: map[string]interface{}{"value": "ok"},
		Git: SingleRecipeGit{
			BaseRepo: "github.com/acme/demo",
			BaseRef:  "main",
		},
	}}
	keys, err := startJobs(context.Background(), swf.JobKey{TenantId: "tenant", JobId: "parent-job"}, contextual.Invocation{NodePath: "node", InvokeSeq: 1}, ctl, "github.com/acme/demo", "main", recipes, "git-ref")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if len(ctl.startRequests) != 1 {
		t.Fatalf("expected 1 StartJob call, got %d", len(ctl.startRequests))
	}
	if ctl.startSawTx[0] {
		t.Fatal("did not expect transaction for single job")
	}
	if got := ctl.startRequests[0].RecipeName; got != "child" {
		t.Fatalf("RecipeName = %q", got)
	}
	if got := ctl.startRequests[0].JobContext.RecipeSource.Repo; got != "github.com/acme/demo" {
		t.Fatalf("RecipeSource.Repo = %q", got)
	}
	if got := ctl.startRequests[0].JobContext.RecipeSource.Ref; got != "main" {
		t.Fatalf("RecipeSource.Ref = %q", got)
	}
}

func TestStartJobsPassesConfiguredArtifactsAsRefsOnly(t *testing.T) {
	artifactRef := recipeartifacts.NewStoredRef(swf.ArtifactKey{
		JobId:       "parent-job",
		TaskOrdinal: 0,
		Name:        "brief.md",
		SizeBytes:   12,
	})
	ctl := &fakeWorkflowControl{}
	recipes := []SingleRecipe{{
		Name:      "child",
		Artifacts: []recipeartifacts.Ref{artifactRef},
		Git: SingleRecipeGit{
			BaseRepo: "github.com/acme/demo",
			BaseRef:  "main",
		},
	}}

	_, err := startJobs(context.Background(), swf.JobKey{TenantId: "tenant", JobId: "parent-job"}, contextual.Invocation{NodePath: "node", InvokeSeq: 1}, ctl, "github.com/acme/demo", "main", recipes, "git-ref")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctl.startRequests) != 1 {
		t.Fatalf("expected 1 StartJob call, got %d", len(ctl.startRequests))
	}

	start := ctl.startRequests[0]
	if len(start.Artifacts) != 0 {
		t.Fatalf("expected forwarded artifact refs not to be attached as concrete job artifacts, got %d", len(start.Artifacts))
	}
	if ctl.getArtifactCalls != 0 {
		t.Fatalf("expected no artifact materialization during child start, got %d calls", ctl.getArtifactCalls)
	}
	if len(start.ArtifactRefs) != 1 {
		t.Fatalf("expected 1 artifact ref, got %d", len(start.ArtifactRefs))
	}
	if got := start.ArtifactRefs[0].Identity(); got != artifactRef.Identity() {
		t.Fatalf("artifact ref identity = %q, want %q", got, artifactRef.Identity())
	}
}

func TestStartJobsMultipleRunsSequentially(t *testing.T) {
	ctl := &fakeWorkflowControl{}
	recipes := []SingleRecipe{
		{
			Name: "child-a",
			Git: SingleRecipeGit{
				BaseRepo: "github.com/acme/demo",
				BaseRef:  "main",
			},
		},
		{
			Name: "child-b",
			Git: SingleRecipeGit{
				BaseRepo: "github.com/acme/demo",
				BaseRef:  "main",
			},
		},
	}
	keys, err := startJobs(context.Background(), swf.JobKey{TenantId: "tenant", JobId: "parent-job"}, contextual.Invocation{NodePath: "node", InvokeSeq: 1}, ctl, "github.com/acme/demo", "main", recipes, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	for i, sawTx := range ctl.startSawTx {
		if sawTx {
			t.Fatalf("did not expect transaction context for call %d", i)
		}
	}
}

func TestStartJobsMultipleErrorReturnsNoKeys(t *testing.T) {
	ctl := &fakeWorkflowControl{
		startErrs: []error{nil, errors.New("boom")},
	}
	recipes := []SingleRecipe{
		{
			Name: "child-a",
			Git: SingleRecipeGit{
				BaseRepo: "github.com/acme/demo",
				BaseRef:  "main",
			},
		},
		{
			Name: "child-b",
			Git: SingleRecipeGit{
				BaseRepo: "github.com/acme/demo",
				BaseRef:  "main",
			},
		},
	}
	keys, err := startJobs(context.Background(), swf.JobKey{TenantId: "tenant", JobId: "parent-job"}, contextual.Invocation{NodePath: "node", InvokeSeq: 1}, ctl, "github.com/acme/demo", "main", recipes, "")
	if err == nil {
		t.Fatal("expected error")
	}
	if keys != nil {
		t.Fatalf("expected nil keys on error, got %v", keys)
	}
}
