package live

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestSubmissionRestartEligibility(t *testing.T) {
	for _, status := range []jobdb.JobStatus{jobdb.JobStatusReady, jobdb.JobStatusCancelled, jobdb.JobStatusCompleted, jobdb.JobStatusActive} {
		for _, executed := range []bool{false, true} {
			run := jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{JobType: "recipe", Status: status}}
			if executed {
				run.Execution = []jobdb.ExecutionHistoryEntry{{Ordinal: 1, Record: jobdb.ExecutionRecord{Kind: "header"}}}
				run.Attempts = []jobdb.JobAttempt{{Tasks: []jobdb.TaskRun{{Attempts: []jobdb.TaskAttempt{{Runtime: &jobdb.TaskRuntime{}}}}}}}
			}
			scope, err := SubmissionRestartScope(run)
			require.NoError(t, err)
			require.Equal(t, "c2j:submission", scope.ScopeID)
			require.Zero(t, scope.ChapterOrdinal)
			run.Job.JobType = "other"
			_, err = SubmissionRestartScope(run)
			require.ErrorIs(t, err, ErrInvalidRestartBoundary)
			run.Job.JobType = "recipe"
			run.Start.Ordinal = 1
			_, err = SubmissionRestartScope(run)
			require.ErrorIs(t, err, ErrInvalidRestartBoundary)
		}
	}
}

func TestSubmissionContextRejectsUnavailableInput(t *testing.T) {
	run := jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{JobType: "recipe"}}
	for _, input := range []*jobdb.TaskIO{nil, {}, {Data: json.RawMessage(`null`)}, {Data: json.RawMessage(`{"inputs":`)}} {
		run.Start.Input = input
		_, err := SubmissionRestartScope(run)
		require.NoError(t, err, "unreadable input does not hide the boundary")
		_, err = InspectSubmissionRestartContext(jobdb.JobKey{TenantId: "tenant", JobId: "job"}, run)
		require.ErrorIs(t, err, ErrRestartContextUnavailable)
	}
}

func TestSubmissionContextRedaction(t *testing.T) {
	run := jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{JobType: "recipe"}, Start: jobdb.JobStart{Input: &jobdb.TaskIO{Data: json.RawMessage(`{"inputs":{"value":"original","api_key":"secret"},"context":{"token":"secret"}}`)}}}
	result, err := InspectSubmissionRestartContext(jobdb.JobKey{TenantId: "tenant", JobId: "job"}, run)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"value": "original", "api_key": "[REDACTED]"}, result.Visible["inputs"])
	require.Equal(t, "[REDACTED]", result.Job["token"])
	require.Equal(t, []string{"/job/token", "/visible/inputs/api_key"}, result.RedactedPaths)
}
