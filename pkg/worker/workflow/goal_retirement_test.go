package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestC02InspectHistoricalProducer(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "B"}
	deadline := time.Date(2026, 11, 20, 1, 2, 3, 4, time.UTC)
	wait := &jobdb.DependencyWaitInfo{WaitID: "saved", State: jobdb.DependencyPending, Deadline: &deadline, Dependencies: []jobdb.DependencyResolution{
		{Target: jobdb.DependencyTarget{GoalID: "G"}, Condition: jobdb.JobPrereqSuccess, State: jobdb.DependencySatisfied, SatisfiedByJobID: "B"},
		{Target: jobdb.DependencyTarget{JobID: "literal-pending"}, Condition: jobdb.JobPrereqComplete, State: jobdb.DependencyPending},
	}}
	artifact := jobdb.ArtifactKey{JobId: "B", TaskOrdinal: 9, Name: "result.txt", SizeBytes: 6}
	reason := "  exact failure\n请保留 ☃\n "
	raw := []byte(`{"producer":"B","other_goal_ids":["opaque"],"n":9007199254740993}`)
	for _, status := range []jobdb.JobStatus{jobdb.JobStatusCompleted, jobdb.JobStatusCancelled} {
		run := jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{JobKey: key, GoalID: "H", OtherGoalIDs: []string{"G"}, BlockingWait: wait, Status: status, CompletionDetail: reason}, Attempts: []jobdb.JobAttempt{{Attempt: 1, Ordinal: 9, Outcome: jobdb.TaskOutcome{Status: jobdb.TaskOutcomeStatusFailed, Error: &jobdb.TaskError{Kind: jobdb.TaskErrorKindApp, Message: reason}}, Output: &jobdb.TaskIO{Data: raw, Artifacts: []jobdb.ArtifactInfo{{Key: &artifact}}}}}}
		ctl := &SWFWorkflowControl{Engine: historyEngine{run: run}}
		got, err := ctl.InspectJob(context.Background(), key)
		require.NoError(t, err)
		require.Empty(t, got.OtherGoalIDs)
		require.Equal(t, "H", got.GoalID)
		require.Equal(t, wait, got.BlockingWait)
		require.Equal(t, reason, got.FailureMessage)
		require.True(t, got.Terminal)
		data, err := got.Output.GetData()
		require.NoError(t, err)
		require.Equal(t, string(raw), string(data))
		arts, err := got.Output.GetArtifacts()
		require.NoError(t, err)
		require.Len(t, arts, 1)
		k, err := arts[0].ArtifactKey()
		require.NoError(t, err)
		require.Equal(t, artifact, k)
		require.Equal(t, []string{"G"}, run.Job.OtherGoalIDs)
	}
}
