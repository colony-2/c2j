package starter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/recipe-core/pkg/task"
	"github.com/colony-2/swf-go/pkg/swf"
)

type captureEngine struct {
	last *swf.SubmitRestartJob
}

func (c *captureEngine) SubmitRestartJob(_ context.Context, req swf.SubmitRestartJob) (swf.JobKey, error) {
	c.last = &req
	return swf.JobKey{TenantId: req.PriorJobKey.TenantId, JobId: "restarted"}, nil
}

func TestRestartRecipeJob_NoPatch(t *testing.T) {
	engine := &captureEngine{}
	prior := swf.JobKey{TenantId: "t1", JobId: "j1"}

	_, err := RestartRecipeJob(context.Background(), engine, prior, 3, nil)
	if err != nil {
		t.Fatalf("RestartRecipeJob: %v", err)
	}
	if engine.last == nil {
		t.Fatalf("expected SubmitRestartJob call")
	}
	if engine.last.PriorJobKey != prior {
		t.Fatalf("unexpected prior job key: %#v", engine.last.PriorJobKey)
	}
	if engine.last.LastStepToKeep != 2 {
		t.Fatalf("expected LastStepToKeep=2, got %d", engine.last.LastStepToKeep)
	}
	if engine.last.ExtraTaskOutput != nil {
		t.Fatalf("expected no ExtraTaskOutput")
	}
}

func TestRestartRecipeJob_WithPatch_InjectsEnvelope(t *testing.T) {
	engine := &captureEngine{}
	prior := swf.JobKey{TenantId: "t1", JobId: "j1"}
	patch := &task.ContextPatch{
		Job: map[string]any{"git": map[string]any{"author": "new-author"}},
	}

	_, err := RestartRecipeJob(context.Background(), engine, prior, 0, patch)
	if err != nil {
		t.Fatalf("RestartRecipeJob: %v", err)
	}
	if engine.last == nil {
		t.Fatalf("expected SubmitRestartJob call")
	}
	if engine.last.LastStepToKeep != -1 {
		t.Fatalf("expected LastStepToKeep=-1 for stepOffset=0, got %d", engine.last.LastStepToKeep)
	}
	if engine.last.ExtraTaskInput == nil {
		t.Fatalf("expected ExtraTaskInput")
	}
	if engine.last.ExtraTaskOutput == nil {
		t.Fatalf("expected ExtraTaskOutput")
	}

	raw, err := engine.last.ExtraTaskOutput.GetData()
	if err != nil {
		t.Fatalf("get ExtraTaskOutput data: %v", err)
	}
	var env task.OutputEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Kind != task.OutputKindContextPatch {
		t.Fatalf("expected kind %q, got %q", task.OutputKindContextPatch, env.Kind)
	}
	var decoded task.ContextPatch
	if err := env.DecodePayload(&decoded); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if decoded.Job["git"] == nil {
		t.Fatalf("expected decoded job patch")
	}
}
