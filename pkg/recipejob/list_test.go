package recipejob

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
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

func TestListChildRecipeJobsFiltersByParentInvocation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	runtime := toyruntime.New()
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		t.Fatalf("BuildEngine(): %v", err)
	}
	engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: runtime}
	tenantID := "tenant-recipejob-children"
	if err := jobdbschema.Register(ctx, runtime, tenantID); err != nil {
		t.Fatalf("Register schema: %v", err)
	}
	parentHandle, err := runtime.SubmitJob(ctx, jobdb.SubmitJobRequest{
		Job: jobdb.SubmitJob{
			TenantId: tenantID,
			JobID:    "parent-job",
			JobType:  "parent-type",
			Data:     jobdb.NewTaskDataOrPanic(map[string]interface{}{"parent": true}),
		},
		RequestTime: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("submit parent: %v", err)
	}
	lease, err := runtime.GetJobLease(ctx, jobdb.GetJobLeaseRequest{
		JobKey:        parentHandle.JobKey,
		WorkerID:      "child-list-test-worker",
		Capabilities:  []string{"parent-type"},
		LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatalf("get parent lease: %v", err)
	}
	parent := jobcontext.Parent{
		TenantID:       tenantID,
		JobID:          "parent-job",
		JobType:        starter.RecipeJobType,
		OpType:         "command_execution",
		OpStep:         "command_execution",
		InvocationHash: "hash-one",
	}
	submittedAt := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	submitRecipeJobWithParent(t, ctx, leaseSubmitter{lease: lease}, tenantID, "child-one", "https://github.com/acme/boo-alpha.git", "alpha", submittedAt, parent)
	parent.InvocationHash = "hash-two"
	submitRecipeJobWithParent(t, ctx, leaseSubmitter{lease: lease}, tenantID, "child-two", "https://github.com/acme/boo-alpha.git", "alpha", submittedAt, parent)

	resp, err := ListChildRecipeJobs(ctx, engine, ListChildRecipeJobsRequest{
		TenantID:             tenantID,
		ParentTenantID:       tenantID,
		ParentJobID:          "parent-job",
		ParentInvocationHash: "hash-one",
	})
	if err != nil {
		t.Fatalf("ListChildRecipeJobs(): %v", err)
	}
	if len(resp.Jobs) != 1 || resp.Jobs[0].JobID != "child-one" {
		t.Fatalf("unexpected scoped children: %#v", resp.Jobs)
	}
	if resp.Jobs[0].Parent == nil || resp.Jobs[0].Parent.InvocationHash != "hash-one" {
		t.Fatalf("parent context not decoded: %#v", resp.Jobs[0].Parent)
	}

	resp, err = ListChildRecipeJobs(ctx, engine, ListChildRecipeJobsRequest{
		TenantID:             tenantID,
		ParentTenantID:       tenantID,
		ParentJobID:          "parent-job",
		AllParentInvocations: true,
	})
	if err != nil {
		t.Fatalf("ListChildRecipeJobs(all ops): %v", err)
	}
	if len(resp.Jobs) != 2 {
		t.Fatalf("len(all ops children) = %d, jobs = %#v", len(resp.Jobs), resp.Jobs)
	}
}

func newListTestEngine(t *testing.T) jobworkflow.Engine {
	t.Helper()

	runtime := toyruntime.New()
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		t.Fatalf("BuildEngine(): %v", err)
	}
	return jobdbschema.WorkflowEngine{Engine: engine, Registry: runtime}
}

func submitRecipeJob(t *testing.T, ctx context.Context, engine jobworkflow.Engine, tenantID string, jobID string, repo string, cellName string, submittedAt time.Time) {
	t.Helper()
	submitRecipeJobWithParent(t, ctx, engine, tenantID, jobID, repo, cellName, submittedAt, jobcontext.Parent{})
}

type testRecipeSubmitter interface {
	SubmitJob(context.Context, jobdb.SubmitJob) (jobdb.JobKey, error)
}

func submitRecipeJobWithParent(t *testing.T, ctx context.Context, engine testRecipeSubmitter, tenantID string, jobID string, repo string, cellName string, submittedAt time.Time, parent jobcontext.Parent) {
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
	if parent.HasJob() {
		start.Parent = &parent
	}
	if _, err := starter.StartRecipeJobWithOptions(ctx, start, engine, starter.StartRecipeJobOptions{JobID: jobID}); err != nil {
		t.Fatalf("StartRecipeJobWithOptions(): %v", err)
	}
}

type leaseSubmitter struct {
	lease jobdb.ExecutionLease
}

func (s leaseSubmitter) SubmitJob(ctx context.Context, job jobdb.SubmitJob) (jobdb.JobKey, error) {
	handle, err := s.lease.SubmitJob(ctx, jobdb.SubmitJobRequest{
		Job:         job,
		RequestTime: time.Now().UTC(),
	})
	if err != nil {
		return jobdb.JobKey{}, err
	}
	return handle.JobKey, nil
}
