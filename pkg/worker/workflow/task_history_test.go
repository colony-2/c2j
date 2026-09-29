package workflow

import (
	"context"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type historyEngine struct {
	jobworkflow.Engine
	run jobdb.GetJobRunResponse
}

func (h historyEngine) GetJobRun(context.Context, jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error) {
	return h.run, nil
}

func TestCachedTaskOutputChecksHistoryIdentity(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	for _, name := range []string{"valid", "wrong job", "wrong type", "wrong hash", "missing ordinal", "failed", "missing output", "foreign artifact"} {
		t.Run(name, func(t *testing.T) {
			attempt := jobdb.TaskAttempt{Ordinal: 3, InputHash: "hash", Outcome: jobdb.TaskOutcome{Status: jobdb.TaskOutcomeStatusSucceeded}, Output: &jobdb.TaskIO{Data: []byte(`{"ok":true}`)}}
			run := jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{JobKey: key}}
			taskType := "op:run"
			switch name {
			case "wrong job":
				run.Job.JobKey.JobId = "other"
			case "wrong type":
				taskType = "other:run"
			case "wrong hash":
				attempt.InputHash = "different"
			case "missing ordinal":
				attempt.Ordinal = 4
			case "failed":
				attempt.Outcome.Status = jobdb.TaskOutcomeStatusFailed
			case "missing output":
				attempt.Output = nil
			case "foreign artifact":
				attempt.Output.Artifacts = []jobdb.ArtifactInfo{{Key: &jobdb.ArtifactKey{JobId: "other", TaskOrdinal: 3, Name: "pack", SizeBytes: 1}}}
			}
			run.Attempts = []jobdb.JobAttempt{{Tasks: []jobdb.TaskRun{{TaskType: taskType, Attempts: []jobdb.TaskAttempt{attempt}}}}}
			control := &SWFWorkflowControl{Engine: historyEngine{run: run}}
			output, err := control.CachedTaskOutput(context.Background(), key, "op:run", 3, "hash")
			if name != "valid" {
				require.Error(t, err)
				require.Nil(t, output)
				return
			}
			require.NoError(t, err)
			raw, err := output.GetData()
			require.NoError(t, err)
			require.JSONEq(t, `{"ok":true}`, string(raw))
		})
	}
}
