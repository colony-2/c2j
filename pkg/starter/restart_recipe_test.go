package starter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
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
	_, err = RestartRecipeJob(context.Background(), engine, jobdb.JobKey{TenantId: "t", JobId: "prior"}, "scope-id", nil, RestartOptions{RequestID: "test-restart"})
	require.NoError(t, err)
	require.NotNil(t, engine.last.ClientPayloadUpdate)
	got, err := execution.PayloadDemand(engine.last.ClientPayloadUpdate.Value)
	require.NoError(t, err)
	require.Equal(t, &d, got)
	require.NotContains(t, string(engine.last.ClientPayloadUpdate.Value), "unrelated")
}

func (c *captureEngine) SubmitRestartJobRef(_ context.Context, request jobdb.SubmitRestartJobRequest) (jobref.Ref, error) {
	req := request.Job
	c.last = &req
	return jobref.Ref{TenantID: req.PriorJobKey.TenantId, JobID: "restarted", GoalID: "goal"}, nil
}

func TestRestartRecipeJob_NoPatch(t *testing.T) {
	engine := &captureEngine{}
	prior := jobdb.JobKey{TenantId: "t1", JobId: "j1"}

	_, err := RestartRecipeJob(context.Background(), engine, prior, "scope-id", nil, RestartOptions{RequestID: "test-restart"})
	if err != nil {
		t.Fatalf("RestartRecipeJob: %v", err)
	}
	if engine.last == nil {
		t.Fatalf("expected SubmitRestartJob call")
	}
	if engine.last.PriorJobKey != prior {
		t.Fatalf("unexpected prior job key: %#v", engine.last.PriorJobKey)
	}
	require.Equal(t, "scope-id", engine.last.ExecutionRestart.ScopeID)
	require.Nil(t, engine.last.ExtraTaskOutput)
	assertHashOnlySchemaSelector(t, engine.last.Schema)
}

func TestRestartRecipeJob_WithPatch_UsesScopePayload(t *testing.T) {
	engine := &captureEngine{}
	prior := jobdb.JobKey{TenantId: "t1", JobId: "j1"}
	patch := &task.ContextPatch{
		Job: map[string]any{"git": map[string]any{"author": "new-author"}},
	}

	_, err := RestartRecipeJob(context.Background(), engine, prior, "scope-id", patch, RestartOptions{RequestID: "test-restart"})
	if err != nil {
		t.Fatalf("RestartRecipeJob: %v", err)
	}
	if engine.last == nil {
		t.Fatalf("expected SubmitRestartJob call")
	}
	require.Equal(t, "scope-id", engine.last.ExecutionRestart.ScopeID)
	require.Nil(t, engine.last.ExtraTaskInput)
	require.Nil(t, engine.last.ExtraTaskOutput)
	var decoded task.ContextPatch
	require.NoError(t, json.Unmarshal(engine.last.ExecutionRestart.Payload, &decoded))
	require.Equal(t, patch.Job, decoded.Job)
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
