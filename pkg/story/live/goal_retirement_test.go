package live

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestC02ReconstructHistoricalProducer(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "B"}
	deadline := time.Date(2026, 11, 20, 1, 2, 3, 4, time.UTC)
	wait := &jobdb.DependencyWaitInfo{WaitID: "saved", State: jobdb.DependencyPending, Deadline: &deadline, Dependencies: []jobdb.DependencyResolution{
		{Target: jobdb.DependencyTarget{GoalID: "G"}, Condition: jobdb.JobPrereqSuccess, State: jobdb.DependencySatisfied, SatisfiedByJobID: "B"},
		{Target: jobdb.DependencyTarget{JobID: "literal-pending"}, Condition: jobdb.JobPrereqComplete, State: jobdb.DependencyPending},
	}}
	artifactKey := jobdb.ArtifactKey{JobId: "B", TaskOrdinal: 9, Name: "result.txt", SizeBytes: 6}
	artifact := jobdb.NewArtifactFromBytes("result.txt", []byte("from B"))
	jobdb.AssignArtifactKey(artifact, artifactKey)
	output := jobdb.NewTaskDataOrPanic(map[string]any{"producer": "B", "other_goal_ids": []string{"opaque"}}, artifact)
	run := executionFixture(jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{JobKey: key, JobType: "recipe", GoalID: "H", OtherGoalIDs: []string{"G"}, BlockingWait: wait}, Attempts: []jobdb.JobAttempt{{Attempt: 1, Tasks: []jobdb.TaskRun{{TaskType: compiler.RootSourceResolutionTaskType, Attempts: []jobdb.TaskAttempt{{Ordinal: 9, Attempt: 1, State: jobdb.TaskAttemptStateSucceeded}}}}}}})
	run.Execution = append(run.Execution, jobdb.ExecutionHistoryEntry{Ordinal: 10, Record: jobdb.ExecutionRecord{Kind: "scope", ID: "saved-restart", Descriptor: json.RawMessage(`{"restart":true,"title":"Saved scope","path":["saved"]}`)}})
	engine := &scriptedStoryEngine{run: run, replay: func(req jobworkflow.ReplayRunRequest) (jobdb.JobData, error) {
		req.Observer.OnJobStart(jobworkflow.JobStartEvent{JobKey: key, AttemptNumber: 1})
		req.Observer.OnTaskStart(jobworkflow.TaskStartEvent{InvocationID: "task:9/attempt:1", JobKey: key, TaskType: compiler.RootSourceResolutionTaskType, Ordinal: 9, AttemptNumber: 1})
		req.Observer.OnTaskEnd(jobworkflow.TaskEndEvent{InvocationID: "task:9/attempt:1", JobKey: key, TaskType: compiler.RootSourceResolutionTaskType, Ordinal: 9, AttemptNumber: 1, Output: output})
		return nil, nil
	}}
	got, err := BuildJobRunStory(context.Background(), engine, key, nil, nil)
	require.NoError(t, err)
	require.Empty(t, got.OtherGoalIDs)
	require.Equal(t, "H", got.GoalID)
	require.Equal(t, wait, got.BlockingWait)
	require.Equal(t, RecordedRestartScopes(run), got.RestartScopes)
	require.NotEmpty(t, got.RestartScopes)
	var find func(*JobRunStoryNode) *JobRunStoryNode
	find = func(n *JobRunStoryNode) *JobRunStoryNode {
		if n == nil {
			return nil
		}
		if len(n.ArtifactKeys) > 0 {
			return n
		}
		for _, child := range n.Children {
			if found := find(child); found != nil {
				return found
			}
		}
		return nil
	}
	node := find(got.Root)
	require.NotNil(t, node)
	require.Equal(t, []jobdb.ArtifactKey{artifactKey}, node.ArtifactKeys)
	require.Equal(t, map[string]any{"producer": "B", "other_goal_ids": []any{"opaque"}}, node.Output)
	require.Equal(t, []string{"G"}, run.Job.OtherGoalIDs)
	var historical JobRunStory
	require.NoError(t, json.Unmarshal([]byte(`{"goal_id":"H","other_goal_ids":["G"]}`), &historical))
	require.Equal(t, []string{"G"}, historical.OtherGoalIDs)
}
