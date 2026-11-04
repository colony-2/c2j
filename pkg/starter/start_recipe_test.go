package starter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type captureSubmitter struct {
	job jobdb.SubmitJob
}

func TestStartRecipeJobDoesNotAddTimeouts(t *testing.T) {
	for _, embedded := range []bool{false, true} {
		name := "runtime-resolved"
		if embedded {
			name = "embedded"
		}
		t.Run(name, func(t *testing.T) {
			engine := &captureSubmitter{}
			var recipes []recipe.Recipe
			if embedded {
				recipes = []recipe.Recipe{{RecipeImpl: &recipe.RecipeOp{
					RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{ID: "unbounded"}},
					OpData:         recipe.OpData{Op: "noop"},
				}}}
			}
			_, err := StartRecipeJobWithOptions(celltest.Context(context.Background()), workflowctl.StartJob{
				TenantId: "tenant", RecipeName: "unbounded", Cell: "test", CellRepo: celltest.Repo(""),
			}, engine, StartRecipeJobOptions{}, recipes...)
			require.NoError(t, err)
			require.Nil(t, engine.job.RunPolicy.TotalTimeout)
			require.Nil(t, engine.job.RunPolicy.InvocationTimeout)
			require.Equal(t, int32(1), engine.job.RunPolicy.Retry.MaximumAttempts)
		})
	}
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

	_, err := StartRecipeJobWithOptions(celltest.Context(context.Background()), workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: "recipe-name", Cell: "test", CellRepo: celltest.Repo(""),
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
	require.Nil(t, engine.job.RunPolicy.InvocationTimeout)
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

func (c *captureSubmitter) SubmitJobRef(_ context.Context, job jobdb.SubmitJob) (jobref.Ref, error) {
	c.job = job
	return jobref.Ref{GoalID: "test-goal", TenantID: job.TenantId, JobID: job.JobID}, nil
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
		}, Cell: "test", CellRepo: celltest.Repo("https://github.com/acme/alpha.git"),
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
	for _, name := range []string{"cell_id", "cell_name", "repo"} {
		if _, exists := m[name]; exists {
			t.Fatalf("ownership metadata %s must be absent", name)
		}
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
		}, Cell: "test", CellRepo: celltest.Repo(""),
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

	_, err := StartRecipeJobWithOptions(celltest.Context(context.Background()), workflowctl.StartJob{
		TenantId:   "tenant",
		JobID:      "child-job-id",
		RecipeName: "recipe-name", Cell: "test", CellRepo: celltest.Repo(""),
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

	_, err := StartRecipeJob(celltest.Context(context.Background()), workflowctl.StartJob{
		TenantId:   "tenant",
		JobID:      "assembled-job-id",
		RecipeName: "recipe-name", Cell: "test", CellRepo: celltest.Repo(""),
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

	_, err := StartRecipeJobWithOptions(celltest.Context(context.Background()), workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: "recipe-name",
		Artifacts: []jobdb.Artifact{
			jobdb.NewArtifactFromBytes("brief.md", []byte("brief")),
		}, Cell: "test", CellRepo: celltest.Repo(""),
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
