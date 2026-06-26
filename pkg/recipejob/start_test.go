package recipejob

import (
	"testing"
	"time"
)

func TestBuildStartJobMatchesCLISemantics(t *testing.T) {
	t.Parallel()

	submittedAt := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	inputs := map[string]interface{}{"prompt": "ship it", "count": float64(2)}
	start, err := BuildStartJob(BuildStartJobRequest{
		TenantID: "tenant-1",
		Target: ResolvedTarget{
			RepositorySource: "https://github.com/acme/boo-alpha.git",
			DefaultRef:       "release",
			CellName:         "alpha",
		},
		Recipe:      "deploy",
		Inputs:      inputs,
		SubmittedAt: &submittedAt,
	})
	if err != nil {
		t.Fatalf("BuildStartJob(): %v", err)
	}

	if start.TenantId != "tenant-1" {
		t.Fatalf("TenantId = %q", start.TenantId)
	}
	if start.RecipeName != "deploy" {
		t.Fatalf("RecipeName = %q", start.RecipeName)
	}
	if start.JobContext.Workflow.CellName != "alpha" {
		t.Fatalf("Workflow.CellName = %q", start.JobContext.Workflow.CellName)
	}
	if start.JobContext.Workflow.ProjectId != "tenant-1" {
		t.Fatalf("Workflow.ProjectId = %q", start.JobContext.Workflow.ProjectId)
	}
	if start.JobContext.GitBase.BaseRepo != "https://github.com/acme/boo-alpha.git" {
		t.Fatalf("GitBase.BaseRepo = %q", start.JobContext.GitBase.BaseRepo)
	}
	if start.JobContext.GitBase.BaseRef != "release" {
		t.Fatalf("GitBase.BaseRef = %q", start.JobContext.GitBase.BaseRef)
	}
	if start.JobContext.RecipeSource.Repo != "https://github.com/acme/boo-alpha.git" {
		t.Fatalf("RecipeSource.Repo = %q", start.JobContext.RecipeSource.Repo)
	}
	if start.JobContext.RecipeSource.Ref != "release" {
		t.Fatalf("RecipeSource.Ref = %q", start.JobContext.RecipeSource.Ref)
	}
	if start.GitRef != "release" {
		t.Fatalf("GitRef = %q", start.GitRef)
	}
	if start.SubmittedAt == nil || !start.SubmittedAt.Equal(submittedAt) {
		t.Fatalf("SubmittedAt = %#v", start.SubmittedAt)
	}
	if start.InputHash != InputHash(inputs) || start.InputHash == "" {
		t.Fatalf("InputHash = %q", start.InputHash)
	}
}

func TestBuildStartJobDefaultsRecipeAndRef(t *testing.T) {
	t.Parallel()

	start, err := BuildStartJob(BuildStartJobRequest{
		TenantID: "tenant-1",
		Target: ResolvedTarget{
			RepositorySource: "https://github.com/acme/boo-alpha.git",
			CellName:         "alpha",
		},
	})
	if err != nil {
		t.Fatalf("BuildStartJob(): %v", err)
	}
	if start.RecipeName != "default" {
		t.Fatalf("RecipeName = %q", start.RecipeName)
	}
	if start.GitRef != "main" {
		t.Fatalf("GitRef = %q", start.GitRef)
	}
}
