package starter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
)

type captureSubmitter struct {
	job swf.SubmitJob
}

func (c *captureSubmitter) SubmitJob(_ context.Context, job swf.SubmitJob) (swf.JobKey, error) {
	c.job = job
	return swf.JobKey{TenantId: job.TenantId, JobId: job.JobID}, nil
}

func TestJobMetadataFromStartJob_JSONFields(t *testing.T) {
	start := workflowctl.StartJob{
		TenantId:   "proj-1",
		RecipeName: "my-recipe",
		GitRef:     "main",
		JobContext: contextual.JobContext{
			Actor: contextual.ActorContext{
				TicketID:   "T-9",
				ActorEmail: "user@example.com",
			},
			Workflow: contextual.WorkflowContext{
				CellID:   "cell-1",
				CellName: "alpha",
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
	if got, _ := m[string(MetaFieldTicketID)].(string); got != "T-9" {
		t.Fatalf("expected %q=%q, got %#v", MetaFieldTicketID, "T-9", m[string(MetaFieldTicketID)])
	}
	if got, _ := m[string(MetaFieldCellID)].(string); got != "cell-1" {
		t.Fatalf("expected %q=%q, got %#v", MetaFieldCellID, "cell-1", m[string(MetaFieldCellID)])
	}
	if got, _ := m[string(MetaFieldCellName)].(string); got != "alpha" {
		t.Fatalf("expected %q=%q, got %#v", MetaFieldCellName, "alpha", m[string(MetaFieldCellName)])
	}
	if got, _ := m[string(MetaFieldActorEmail)].(string); got != "user@example.com" {
		t.Fatalf("expected %q=%q, got %#v", MetaFieldActorEmail, "user@example.com", m[string(MetaFieldActorEmail)])
	}
	if got, _ := m[string(MetaFieldGitRef)].(string); got != "main" {
		t.Fatalf("expected %q=%q, got %#v", MetaFieldGitRef, "main", m[string(MetaFieldGitRef)])
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
