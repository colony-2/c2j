package starter

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/stretchr/testify/require"
	"testing"

	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type captureEngine struct {
	last *jobdb.SubmitRestartJob
	info jobdb.JobInfo
}

func (c *captureEngine) GetJob(context.Context, jobdb.JobKey) (jobdb.JobInfo, error) {
	return c.info, nil
}

func TestRestartPreservesPublishedExecutionRequirements(t *testing.T) {
	memory := "16Gi"
	d, err := execution.Initial(nil, "recipe-digest", execution.Requirements{Resources: execution.Resources{Memory: &memory}})
	require.NoError(t, err)
	d.Revision = 2
	d.Checkpoints = map[string]string{"1:activity:inspect": "hash"}
	raw, err := execution.PayloadWithDemand(json.RawMessage(`{"unrelated":"not inherited"}`), d)
	require.NoError(t, err)
	engine := &captureEngine{info: jobdb.JobInfo{ClientPayload: raw}}
	_, err = RestartRecipeJob(context.Background(), engine, jobdb.JobKey{TenantId: "t", JobId: "prior"}, 2, nil)
	require.NoError(t, err)
	require.NotNil(t, engine.last.ClientPayloadUpdate)
	got, err := execution.PayloadDemand(engine.last.ClientPayloadUpdate.Value)
	require.NoError(t, err)
	require.Equal(t, &d, got)
	require.NotContains(t, string(engine.last.ClientPayloadUpdate.Value), "unrelated")
}

func (c *captureEngine) SubmitRestartJob(_ context.Context, req jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	c.last = &req
	return jobdb.JobKey{TenantId: req.PriorJobKey.TenantId, JobId: "restarted"}, nil
}

func TestRestartRecipeJob_NoPatch(t *testing.T) {
	engine := &captureEngine{}
	prior := jobdb.JobKey{TenantId: "t1", JobId: "j1"}

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
	assertHashOnlySchemaSelector(t, engine.last.Schema)
}

func TestRestartRecipeJob_WithPatch_InjectsEnvelope(t *testing.T) {
	engine := &captureEngine{}
	prior := jobdb.JobKey{TenantId: "t1", JobId: "j1"}
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
	assertHashOnlySchemaSelector(t, engine.last.Schema)
}

func assertHashOnlySchemaSelector(t *testing.T, selector *jobdb.JobSchemaSelector) {
	t.Helper()
	wantSchemaHash, err := jobdbschema.Hash()
	if err != nil {
		t.Fatalf("schema hash: %v", err)
	}
	if selector == nil {
		t.Fatalf("expected schema selector")
	}
	if selector.Hash != wantSchemaHash {
		t.Fatalf("schema hash = %q, want %q", selector.Hash, wantSchemaHash)
	}
	if len(selector.Schema) != 0 {
		t.Fatalf("expected hash-only schema selector")
	}
}
