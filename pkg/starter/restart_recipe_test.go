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
	info jobdb.JobSummary
}

func (c *captureEngine) ListJobs(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{c.info}}, nil
}

func TestRestartUsesInitialRequirementsNotLatestPayload(t *testing.T) {
	initial, err := execution.Initial(nil, true, execution.Requirements{Resources: execution.Resources{Memory: stringPtr("4Gi")}})
	require.NoError(t, err)
	metadata, err := json.Marshal(JobMetadata{Execution: &initial})
	require.NoError(t, err)
	for _, payload := range []json.RawMessage{
		json.RawMessage(`{"unrelated":"not inherited","c2j":{"execution":{"schema_version":3,"recipe_resolution":"resolved","effective":{"resources":{"memory":"16Gi"}}}}}`),
		json.RawMessage(`{"c2j":{"execution":"invalid latest state is irrelevant"}}`),
	} {
		engine := &captureEngine{info: jobdb.JobSummary{Metadata: metadata, ClientPayload: payload}}
		_, err = RestartRecipeJob(context.Background(), engine, jobdb.JobKey{TenantId: "t", JobId: "prior"}, "scope-id", nil, RestartOptions{RequestID: "test-restart"})
		require.NoError(t, err)
		require.NotNil(t, engine.last.ClientPayloadUpdate)
		got, err := execution.PayloadDemand(engine.last.ClientPayloadUpdate.Value)
		require.NoError(t, err)
		require.Equal(t, &initial.Demand, got)
		require.NotContains(t, string(engine.last.ClientPayloadUpdate.Value), "unrelated")
	}
}

func stringPtr(s string) *string { return &s }

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
