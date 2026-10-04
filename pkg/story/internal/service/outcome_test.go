package service

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type outcomeEngine struct {
	jobworkflow.Engine
	run jobdb.GetJobRunResponse
}

func (e outcomeEngine) GetJobRun(context.Context, jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error) {
	return e.run, nil
}

func TestWorkflowOutcomeReconcilesTerminalState(t *testing.T) {
	success := jobdb.JobAttempt{Ordinal: 9, Output: &jobdb.TaskIO{Data: []byte(`{"answer":"yes"}`)}, Outcome: jobdb.TaskOutcome{Status: jobdb.TaskOutcomeStatusSucceeded}}
	failure := jobdb.JobAttempt{Ordinal: 4, Outcome: jobdb.TaskOutcome{Status: jobdb.TaskOutcomeStatusFailed, Error: &jobdb.TaskError{Kind: jobdb.TaskErrorKindApp, Message: "failed attempt"}}}
	timeout := jobdb.JobAttempt{Ordinal: 9, Outcome: jobdb.TaskOutcome{Status: jobdb.TaskOutcomeStatusFailed, Error: &jobdb.TaskError{Kind: jobdb.TaskErrorKindTimeout, Message: "deadline exceeded"}}}
	for _, tc := range []struct {
		name     string
		state    jobdb.JobStatus
		attempts []jobdb.JobAttempt
		want     model.WorkflowStatus
		message  string
		pending  bool
	}{
		{name: "success", state: jobdb.JobStatusCompleted, attempts: []jobdb.JobAttempt{success}, want: model.WorkflowStatusCompleted},
		{name: "failure", state: jobdb.JobStatusCompleted, attempts: []jobdb.JobAttempt{failure}, want: model.WorkflowStatusFailed, message: "failed attempt"},
		{name: "timeout", state: jobdb.JobStatusCompleted, attempts: []jobdb.JobAttempt{timeout}, want: model.WorkflowStatusFailed, message: "deadline exceeded"},
		{name: "successful retry", state: jobdb.JobStatusCompleted, attempts: []jobdb.JobAttempt{failure, success}, want: model.WorkflowStatusCompleted},
		{name: "exhausted retry", state: jobdb.JobStatusCompleted, attempts: []jobdb.JobAttempt{failure, timeout}, want: model.WorkflowStatusFailed, message: "deadline exceeded"},
		{name: "cancelled after failure", state: jobdb.JobStatusCancelled, attempts: []jobdb.JobAttempt{failure}, want: model.WorkflowStatusCanceled, message: "failed attempt"},
		{name: "cancelled before execution", state: jobdb.JobStatusCancelled, want: model.WorkflowStatusCanceled},
		{name: "expired", state: jobdb.JobStatusExpired, attempts: []jobdb.JobAttempt{failure}, want: model.WorkflowStatusTimedOut, message: "failed attempt"},
		{name: "crash concern", state: jobdb.JobStatusCrashConcern, want: model.WorkflowStatusFailed},
		{name: "active retry", state: jobdb.JobStatusActive, attempts: []jobdb.JobAttempt{failure}, pending: true},
		{name: "ready retry", state: jobdb.JobStatusReady, attempts: []jobdb.JobAttempt{failure}, pending: true},
		{name: "retry backoff", state: jobdb.JobStatusAwaitingFuture, attempts: []jobdb.JobAttempt{failure}, pending: true},
		{name: "waiting children", state: jobdb.JobStatusPendingJobs, attempts: []jobdb.JobAttempt{failure}, pending: true},
		{name: "no attempts yet", state: jobdb.JobStatusReady, pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, err := New(Config{Engine: outcomeEngine{run: jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{Status: tc.state}, Attempts: tc.attempts}}})
			require.NoError(t, err)
			out, err := service.GetWorkflowOutcome(context.Background(), model.GetWorkflowOutcomeRequest{ProjectID: "tenant", JobID: "job"})
			if tc.pending {
				require.ErrorIs(t, err, ErrOutcomePending)
				require.Nil(t, out)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, out.Status)
			if tc.message == "" {
				require.Nil(t, out.Error)
			} else {
				require.NotNil(t, out.Error)
				require.Equal(t, tc.message, *out.Error)
			}
			if len(tc.attempts) > 0 {
				require.Equal(t, tc.attempts[len(tc.attempts)-1].Ordinal, *out.AttemptOrdinal)
			}
			if tc.want == model.WorkflowStatusCompleted {
				require.Equal(t, "yes", out.Output["answer"])
			}
		})
	}
}
