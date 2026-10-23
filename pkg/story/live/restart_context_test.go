package live

import (
	"context"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRestartContextUnavailableIsNotEmptyContext(t *testing.T) {
	history := []jobdb.ExecutionHistoryEntry{{Ordinal: 7, Record: jobdb.ExecutionRecord{Kind: "scope", ID: "scope", Descriptor: []byte(`{"restart":true}`)}}}
	for _, cause := range []error{nil, jobworkflow.ReplayCacheMissError{Reason: jobworkflow.ReplayCacheMissTaskResultMissing}, jobdb.ErrWorkflowNotDeterministic, errors.New("artifact unavailable")} {
		engine := &scriptedStoryEngine{replay: func(jobworkflow.ReplayRunRequest) (jobdb.JobData, error) { return nil, cause }}
		result, err := InspectRestartContext(context.Background(), engine, jobdb.JobKey{TenantId: "tenant", JobId: "job"}, history, 7, nil, nil)
		require.Nil(t, result)
		require.ErrorIs(t, err, ErrRestartContextUnavailable)
		if cause != nil {
			require.ErrorIs(t, err, cause)
		}
	}
}

func TestRestartContextRedactionPaths(t *testing.T) {
	paths := []string{}
	value := map[string]any{"normal": "keep", "empty": nil, "a/b~c": []any{map[string]any{"password": "hidden"}}}
	filtered := redactRestartValue("", "/visible", value, &paths).(map[string]any)
	require.Equal(t, "keep", filtered["normal"])
	require.Nil(t, filtered["empty"])
	require.Equal(t, []string{"/visible/a~1b~0c/0/password"}, paths)
	require.Equal(t, "hidden", value["a/b~c"].([]any)[0].(map[string]any)["password"], "redaction must not mutate the source")
}

func TestCopyAttemptPreservesLogicalInvocationIdentity(t *testing.T) {
	step := &model.JobRunStoryNode{}
	copyAttemptIntoStep(step, &model.JobRunStoryNode{InvocationID: "source-task/attempt:1", Attempt: 1})
	require.Equal(t, "source-task", step.InvocationID)
	copyAttemptIntoStep(step, &model.JobRunStoryNode{InvocationID: "source-task/attempt:2", Attempt: 2})
	require.Equal(t, "source-task", step.InvocationID, "step identity must not become an attempt identity")
	require.Equal(t, 2, step.Attempt)
}

func TestCompactRestartBoundaryRequiresScopeIDWhenAmbiguous(t *testing.T) {
	history := []jobdb.ExecutionHistoryEntry{
		{Ordinal: 7, Record: jobdb.ExecutionRecord{Kind: "scope", ID: "first", Descriptor: []byte(`{"restart":true}`)}},
		{Ordinal: 7, Record: jobdb.ExecutionRecord{Kind: "scope", ID: "second", Descriptor: []byte(`{"restart":true}`)}},
	}
	_, err := RestartBoundary(history, 7)
	require.ErrorIs(t, err, ErrInvalidRestartBoundary)
	for _, id := range []string{"first", "second"} {
		entry, err := RestartBoundaryByID(history, id)
		require.NoError(t, err)
		require.Equal(t, id, entry.Record.ID)
	}
	_, err = RestartBoundaryByID(history, "missing")
	require.ErrorIs(t, err, ErrInvalidRestartBoundary)
}
