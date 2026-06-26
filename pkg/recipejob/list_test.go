package recipejob

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/starter"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func TestListRecipeJobsFiltersByNormalizedRepository(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	engine := newListTestEngine(t)
	tenantID := "tenant-recipejob-list"
	submittedAt := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

	submitRecipeJob(t, ctx, engine, tenantID, "job-alpha", "https://github.com/acme/boo-alpha.git", "alpha", submittedAt)
	submitRecipeJob(t, ctx, engine, tenantID, "job-beta", "https://github.com/acme/boo-beta.git", "beta", submittedAt)

	resp, err := ListRecipeJobs(ctx, engine, ListRecipeJobsRequest{
		TenantID:         tenantID,
		RepositorySource: "github.com/acme/boo-alpha",
	})
	if err != nil {
		t.Fatalf("ListRecipeJobs(): %v", err)
	}

	if len(resp.Jobs) != 1 {
		t.Fatalf("len(Jobs) = %d, jobs = %#v", len(resp.Jobs), resp.Jobs)
	}
	job := resp.Jobs[0]
	if job.JobID != "job-alpha" {
		t.Fatalf("JobID = %q", job.JobID)
	}
	if job.RepositorySource != "https://github.com/acme/boo-alpha.git" {
		t.Fatalf("RepositorySource = %q", job.RepositorySource)
	}
	if job.CellName != "alpha" {
		t.Fatalf("CellName = %q", job.CellName)
	}
	if job.RecipeName != "deploy" {
		t.Fatalf("RecipeName = %q", job.RecipeName)
	}
	if job.GitRef != "main" {
		t.Fatalf("GitRef = %q", job.GitRef)
	}
	if job.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt is zero")
	}
}

func TestListRecipeJobsFiltersByResolvedShortName(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	root := t.TempDir()
	writeConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: alpha
`)

	target, err := ResolveTarget(ctx, ResolveTargetRequest{
		WorkingDir: root,
		Cell:       "beta",
	})
	if err != nil {
		t.Fatalf("ResolveTarget(): %v", err)
	}

	engine := newListTestEngine(t)
	tenantID := "tenant-recipejob-short"
	submittedAt := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	submitRecipeJob(t, ctx, engine, tenantID, "job-alpha", "https://github.com/acme/boo-alpha.git", "alpha", submittedAt)
	submitRecipeJob(t, ctx, engine, tenantID, "job-beta", "https://github.com/acme/boo-beta.git", "beta", submittedAt)

	resp, err := ListRecipeJobs(ctx, engine, ListRecipeJobsRequest{
		TenantID:         tenantID,
		RepositorySource: target.RepositorySource,
	})
	if err != nil {
		t.Fatalf("ListRecipeJobs(): %v", err)
	}

	if len(resp.Jobs) != 1 || resp.Jobs[0].JobID != "job-beta" {
		t.Fatalf("unexpected jobs: %#v", resp.Jobs)
	}
}

func TestGetRecipeJobReadsOneJob(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	engine := newListTestEngine(t)
	tenantID := "tenant-recipejob-get"
	submittedAt := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	submitRecipeJob(t, ctx, engine, tenantID, "job-alpha", "https://github.com/acme/boo-alpha.git", "alpha", submittedAt)

	job, err := GetRecipeJob(ctx, engine, GetRecipeJobRequest{
		TenantID: tenantID,
		JobID:    "job-alpha",
	})
	if err != nil {
		t.Fatalf("GetRecipeJob(): %v", err)
	}
	if job.JobID != "job-alpha" || job.RepositorySource != "https://github.com/acme/boo-alpha.git" {
		t.Fatalf("unexpected job: %#v", job)
	}
}

func newListTestEngine(t *testing.T) jobworkflow.Engine {
	t.Helper()

	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(toyruntime.New()).BuildEngine()
	if err != nil {
		t.Fatalf("BuildEngine(): %v", err)
	}
	return engine
}

func submitRecipeJob(t *testing.T, ctx context.Context, engine jobworkflow.Engine, tenantID string, jobID string, repo string, cellName string, submittedAt time.Time) {
	t.Helper()

	start, err := BuildStartJob(BuildStartJobRequest{
		TenantID: tenantID,
		Target: ResolvedTarget{
			RepositorySource: repo,
			DefaultRef:       "main",
			CellName:         cellName,
		},
		Recipe:      "deploy",
		Inputs:      map[string]interface{}{"prompt": cellName},
		SubmittedAt: &submittedAt,
	})
	if err != nil {
		t.Fatalf("BuildStartJob(): %v", err)
	}
	if _, err := starter.StartRecipeJobWithOptions(ctx, start, engine, starter.StartRecipeJobOptions{JobID: jobID}); err != nil {
		t.Fatalf("StartRecipeJobWithOptions(): %v", err)
	}
}
