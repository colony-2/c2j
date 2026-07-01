package starter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type captureSubmitter struct {
	job jobdb.SubmitJob
}

func TestStartRecipeJobUsesRecipeTimeoutAsDurableJobTotalTimeout(t *testing.T) {
	engine := &captureSubmitter{}
	rec := recipe.Recipe{RecipeImpl: &recipe.RecipeOp{
		RecipeMetadata: recipe.RecipeMetadata{
			NodeMetadata: recipe.NodeMetadata{
				ID:      "recipe-name",
				Timeout: recipe.Duration(2 * time.Minute),
				Inputs:  map[string]interface{}{},
			},
		},
		OpData: recipe.OpData{Op: "noop"},
	}}

	_, err := StartRecipeJobWithOptions(context.Background(), workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: "recipe-name",
	}, engine, StartRecipeJobOptions{}, rec)
	if err != nil {
		t.Fatalf("StartRecipeJobWithOptions: %v", err)
	}

	if engine.job.RunPolicy.TotalTimeout == nil {
		t.Fatal("expected total timeout to be set")
	}
	if got := time.Duration(*engine.job.RunPolicy.TotalTimeout); got != 2*time.Minute {
		t.Fatalf("expected total timeout %s, got %s", 2*time.Minute, got)
	}
	wantSchemaHash, err := jobdbschema.Hash()
	if err != nil {
		t.Fatalf("schema hash: %v", err)
	}
	if engine.job.Schema == nil {
		t.Fatalf("expected schema selector")
	}
	if engine.job.Schema.Hash != wantSchemaHash {
		t.Fatalf("schema hash = %q, want %q", engine.job.Schema.Hash, wantSchemaHash)
	}
	if len(engine.job.Schema.Schema) != 0 {
		t.Fatalf("expected hash-only schema selector")
	}
}

func (c *captureSubmitter) SubmitJob(_ context.Context, job jobdb.SubmitJob) (jobdb.JobKey, error) {
	c.job = job
	return jobdb.JobKey{TenantId: job.TenantId, JobId: job.JobID}, nil
}

func TestJobMetadataFromStartJob_JSONFields(t *testing.T) {
	start := workflowctl.StartJob{
		TenantId:   "proj-1",
		RecipeName: "my-recipe",
		GitRef:     "main",
		JobContext: contextual.JobContext{
			Workflow: contextual.WorkflowContext{
				CellID:   "cell-1",
				CellName: "alpha",
			},
			GitBase: contextual.GitBaseContext{
				BaseRepo: "https://github.com/acme/alpha.git",
			},
		},
	}

	meta := JobMetadataFromStartJob(start)
	if meta.Version != JobMetadataVersion {
		t.Fatalf("expected version %d, got %d", JobMetadataVersion, meta.Version)
	}

	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal metadata map: %v", err)
	}

	if got, _ := m[string(MetaFieldRecipe)].(string); got != "my-recipe" {
		t.Fatalf("expected %q=%q, got %#v", MetaFieldRecipe, "my-recipe", m[string(MetaFieldRecipe)])
	}
	if got, _ := m[string(MetaFieldCellID)].(string); got != "cell-1" {
		t.Fatalf("expected %q=%q, got %#v", MetaFieldCellID, "cell-1", m[string(MetaFieldCellID)])
	}
	if got, _ := m[string(MetaFieldCellName)].(string); got != "alpha" {
		t.Fatalf("expected %q=%q, got %#v", MetaFieldCellName, "alpha", m[string(MetaFieldCellName)])
	}
	if got, _ := m[string(MetaFieldRepo)].(string); got != "https://github.com/acme/alpha.git" {
		t.Fatalf("expected %q=%q, got %#v", MetaFieldRepo, "https://github.com/acme/alpha.git", m[string(MetaFieldRepo)])
	}
	if got, _ := m[string(MetaFieldGitRef)].(string); got != "main" {
		t.Fatalf("expected %q=%q, got %#v", MetaFieldGitRef, "main", m[string(MetaFieldGitRef)])
	}
}

func TestJobMetadataFromStartJobIncludesParent(t *testing.T) {
	start := workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: "child",
		Parent: &jobcontext.Parent{
			TenantID:           "tenant",
			JobID:              "parent-job",
			JobType:            RecipeJobType,
			OpType:             "command_execution",
			OpStep:             "command_execution",
			OpTaskType:         "command_execution:command_execution",
			CellName:           "alpha",
			RepositorySource:   "github.com/acme/alpha",
			GitRef:             "main",
			InvocationPath:     "root/step",
			InvocationSequence: 3,
			InvocationHash:     "abc123",
		},
	}

	meta := JobMetadataFromStartJob(start)
	if meta.ParentTenantID != "tenant" || meta.ParentJobID != "parent-job" || meta.ParentInvocationHash != "abc123" {
		t.Fatalf("unexpected parent metadata: %#v", meta)
	}
	if meta.ParentInvocationSequence != 3 {
		t.Fatalf("ParentInvocationSequence = %d", meta.ParentInvocationSequence)
	}
}

func TestStartRecipeJobWithOptions_ForwardsExplicitJobID(t *testing.T) {
	engine := &captureSubmitter{}

	_, err := StartRecipeJobWithOptions(context.Background(), workflowctl.StartJob{
		TenantId:   "tenant",
		JobID:      "child-job-id",
		RecipeName: "recipe-name",
	}, engine, StartRecipeJobOptions{
		JobID: "child-job-id",
	})
	if err != nil {
		t.Fatalf("StartRecipeJobWithOptions: %v", err)
	}

	if engine.job.JobID != "child-job-id" {
		t.Fatalf("expected explicit job id to be forwarded, got %q", engine.job.JobID)
	}
}

func TestStartRecipeJobUsesStartJobIDWhenOptionsJobIDEmpty(t *testing.T) {
	engine := &captureSubmitter{}

	_, err := StartRecipeJob(context.Background(), workflowctl.StartJob{
		TenantId:   "tenant",
		JobID:      "assembled-job-id",
		RecipeName: "recipe-name",
	}, engine)
	if err != nil {
		t.Fatalf("StartRecipeJob: %v", err)
	}

	if engine.job.JobID != "assembled-job-id" {
		t.Fatalf("expected start job id to be forwarded, got %q", engine.job.JobID)
	}
}

func TestStartRecipeJobAttachesArtifactsWithoutSerializingThemInPayload(t *testing.T) {
	engine := &captureSubmitter{}

	_, err := StartRecipeJobWithOptions(context.Background(), workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: "recipe-name",
		Artifacts: []jobdb.Artifact{
			jobdb.NewArtifactFromBytes("brief.md", []byte("brief")),
		},
	}, engine, StartRecipeJobOptions{})
	if err != nil {
		t.Fatalf("StartRecipeJobWithOptions: %v", err)
	}

	raw, err := engine.job.Data.GetData()
	if err != nil {
		t.Fatalf("GetData(): %v", err)
	}
	var payload workflowctl.StartJob
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal start payload: %v", err)
	}
	if len(payload.Artifacts) != 0 {
		t.Fatalf("payload serialized artifacts: %#v", payload.Artifacts)
	}

	artifacts, err := engine.job.Data.GetArtifacts()
	if err != nil {
		t.Fatalf("GetArtifacts(): %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Name() != "brief.md" {
		t.Fatalf("attached artifacts = %#v", artifacts)
	}
}
