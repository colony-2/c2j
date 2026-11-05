package live

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestSubmissionRestartRequiresExecutionAfterSubmission(t *testing.T) {
	run := jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{JobType: "recipe"}}
	_, err := SubmissionRestartScope(run)
	require.ErrorIs(t, err, ErrInvalidRestartBoundary, "submission-only histories cannot be restarted by JobDB")
	run.Execution = []jobdb.ExecutionHistoryEntry{{Ordinal: 1, Record: jobdb.ExecutionRecord{Kind: "header"}}}
	_, err = SubmissionRestartScope(run)
	require.NoError(t, err, "no recipe scope is needed")
	run.Execution = nil
	run.Attempts = []jobdb.JobAttempt{{}}
	_, err = SubmissionRestartScope(run)
	require.ErrorIs(t, err, ErrInvalidRestartBoundary, "an initial attempt is synthesized before execution")
	run.Attempts[0].Ordinal = 1
	_, err = SubmissionRestartScope(run)
	require.NoError(t, err, "older histories with job attempts are supported")
	run.Job.JobType = "other"
	_, err = SubmissionRestartScope(run)
	require.ErrorIs(t, err, ErrInvalidRestartBoundary)
}

func TestSubmissionContextRejectsUnavailableInput(t *testing.T) {
	run := jobdb.GetJobRunResponse{
		Job:       jobdb.JobRunSummary{JobType: "recipe"},
		Execution: []jobdb.ExecutionHistoryEntry{{Ordinal: 1, Record: jobdb.ExecutionRecord{Kind: "header"}}},
	}
	for _, input := range []*jobdb.TaskIO{nil, {}, {Data: json.RawMessage(`null`)}, {Data: json.RawMessage(`{"inputs":`)}} {
		run.Start.Input = input
		_, err := InspectSubmissionRestartContext(jobdb.JobKey{TenantId: "tenant", JobId: "job"}, run)
		require.ErrorIs(t, err, ErrRestartContextUnavailable)
	}
}
