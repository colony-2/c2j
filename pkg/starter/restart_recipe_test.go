package starter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type captureEngine struct {
	last        *jobdb.SubmitRestartJob
	info        jobdb.JobSummary
	lookup      *jobdb.ListJobsRequest
	lookupError error
	missing     bool
}

func (c *captureEngine) ListJobs(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	c.lookup = &req
	if c.lookupError != nil || c.missing {
		return jobdb.ListJobsResponse{}, c.lookupError
	}
	return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{c.info}}, nil
}

func TestRestartRecipeJobExactSourceMetadata(t *testing.T) {
	prior := jobdb.JobKey{TenantId: "tenant", JobId: "hidden-source"}
	for _, goal := range []string{"", "new"} {
		t.Run("goal="+goal, func(t *testing.T) {
			engine := &captureEngine{info: jobdb.JobSummary{Cell: "source-cell"}}
			_, err := RestartRecipeJob(context.Background(), engine, prior, "scope", nil, RestartOptions{Goal: goal, RequestID: "request"})
			require.NoError(t, err)
			require.Equal(t, &jobdb.ListJobsRequest{
				TenantIds: []string{prior.TenantId}, JobKeys: []jobdb.JobKey{prior},
				IncludeHidden: true, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}, PageSize: 1,
			}, engine.lookup, "hidden metadata access must stay limited to the exact source")
			require.NotNil(t, engine.last)
			require.Equal(t, prior, engine.last.PriorJobKey)
			require.Equal(t, "source-cell", engine.last.Cell)
			require.False(t, engine.last.ShareGoal)
		})
	}
}

func TestRestartRecipeJobMetadataFailureNeverSubmits(t *testing.T) {
	backendError := errors.New("metadata backend unavailable")
	for _, fixture := range []struct {
		name   string
		engine captureEngine
		want   error
	}{
		{name: "missing", engine: captureEngine{missing: true}, want: jobdb.ErrJobNotFound},
		{name: "read error", engine: captureEngine{lookupError: backendError}, want: backendError},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			engine := fixture.engine
			_, err := RestartRecipeJob(context.Background(), &engine, jobdb.JobKey{TenantId: "tenant", JobId: "source"}, "scope", nil, RestartOptions{RequestID: "request"})
			require.ErrorIs(t, err, fixture.want)
			require.Nil(t, engine.last)
		})
	}
}

func TestRestartRecipeJobInvalidOptionsNeverSubmit(t *testing.T) {
	for _, fixture := range []struct {
		name, scope, goal, requestID, want string
	}{
		{name: "blank scope", scope: " \t", requestID: "request", want: "scope ID is required"},
		{name: "unsupported goal", scope: "scope", goal: "other", requestID: "request", want: "goal sharing is unsupported"},
		{name: "blank request", scope: "scope", requestID: " \t", want: "restart request ID is required"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			engine := &captureEngine{}
			_, err := RestartRecipeJob(context.Background(), engine, jobdb.JobKey{TenantId: "tenant", JobId: "source"}, fixture.scope, nil, RestartOptions{Goal: fixture.goal, RequestID: fixture.requestID})
			require.ErrorContains(t, err, fixture.want)
			require.Nil(t, engine.last)
			if fixture.name == "blank scope" {
				require.Nil(t, engine.lookup)
			}
		})
	}
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
		_, err = RestartRecipeJob(celltest.Context(context.Background()), engine, jobdb.JobKey{TenantId: "t", JobId: "prior"}, "scope-id", nil, RestartOptions{RequestID: "test-restart"})
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
	return jobref.Ref{TenantID: req.PriorJobKey.TenantId, JobID: "restarted", GoalID: "goal", Cell: func() string {
		if req.Cell != "" {
			return req.Cell
		}
		return "test"
	}(), CellID: "cell", CellRepo: "example.test/test"}, nil
}

func TestRestartRecipeJob_NoPatch(t *testing.T) {
	engine := &captureEngine{}
	prior := jobdb.JobKey{TenantId: "t1", JobId: "j1"}

	_, err := RestartRecipeJob(celltest.Context(context.Background()), engine, prior, "scope-id", nil, RestartOptions{RequestID: "test-restart"})
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
		{Op: "add", Path: "/context/git/author", Value: []byte(`"new-author"`)},
	}

	_, err := RestartRecipeJob(celltest.Context(context.Background()), engine, prior, "scope-id", patch, RestartOptions{RequestID: "test-restart"})
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
	require.Equal(t, *patch, decoded)
	assertHashOnlySchemaSelector(t, engine.last.Schema)
}

func TestRestartRecipeJob_FromSubmission(t *testing.T) {
	for _, patch := range []*task.ContextPatch{nil, {}} {
		engine := &captureEngine{info: jobdb.JobSummary{Cell: "source-cell"}}
		_, err := RestartRecipeJob(context.Background(), engine, jobdb.JobKey{TenantId: "t", JobId: "prior"}, SubmissionRestartScopeID, patch, RestartOptions{RequestID: "submission-restart"})
		require.NoError(t, err)
		require.Nil(t, engine.last.ExecutionRestart)
		require.Zero(t, engine.last.LastStepToKeep)
		require.Equal(t, "source-cell", engine.last.Cell)
		require.False(t, engine.last.ShareGoal)
		require.Nil(t, engine.last.ExtraTaskInput)
		require.Nil(t, engine.last.ExtraTaskOutput)
		assertHashOnlySchemaSelector(t, engine.last.Schema)
	}
	for _, patch := range []*task.ContextPatch{
		{{Op: "add", Path: "/context/git/author", Value: []byte(`"changed"`)}},
		{{Op: "test", Path: "/sequence/step/outputs", Value: []byte(`{}`)}},
	} {
		engine := &captureEngine{}
		_, err := RestartRecipeJob(context.Background(), engine, jobdb.JobKey{TenantId: "t", JobId: "prior"}, SubmissionRestartScopeID, patch, RestartOptions{RequestID: "submission-restart"})
		require.ErrorContains(t, err, "submission restart does not support context patches")
		require.Nil(t, engine.last, "must not submit or silently discard a patch")
	}
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

func TestRestartRecipeJobInvalidPatchBeforeMetadataOrRetry(t *testing.T) {
	patch := task.ContextPatch{{Op: "test", Path: "/context/workspace", Value: []byte(`null`)}}
	// No engine is needed: validation must precede all backend capability checks.
	_, err := RestartRecipeJob(context.Background(), nil, jobdb.JobKey{}, "scope", &patch, RestartOptions{RequestID: "accepted-id"})
	require.ErrorContains(t, err, "identity cannot be patched")
}
