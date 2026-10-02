package compiler

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type timeoutReplayEntry struct {
	taskType string
	input    string
	output   jobdb.TaskData
}

type timeoutReplayContext struct {
	policyCaptureJobContext
	t       *testing.T
	entries []timeoutReplayEntry
	cursor  int
	live    int
}

func (c *timeoutReplayContext) DoTask(policy jobdb.RunPolicy, taskType string, input jobdb.TaskData) (jobdb.TaskData, error) {
	raw, err := input.GetData()
	require.NoError(c.t, err)
	if taskType != TimeoutCheckpointTaskType {
		c.policies = append(c.policies, policy)
	}
	ordinal := c.cursor
	c.cursor++
	if ordinal < len(c.entries) {
		e := c.entries[ordinal]
		require.Equal(c.t, e.taskType, taskType)
		require.JSONEq(c.t, e.input, string(raw))
		return e.output, nil
	}
	var output jobdb.TaskData
	if taskType == TimeoutCheckpointTaskType {
		output, err = (timeoutCheckpointWorker{}).Run(jobworkflow.TaskContext{}, input)
	} else {
		c.live++
		output = input
	}
	require.NoError(c.t, err)
	c.entries = append(c.entries, timeoutReplayEntry{taskType, string(raw), output})
	return output, nil
}

func TestDurableTimeoutReplaysCompletedTaskAndRejectsNewWork(t *testing.T) {
	ctx := &timeoutReplayContext{t: t}
	first, err := withDurableExecutionTimeout(ctx, 30*time.Millisecond, "scope")
	require.NoError(t, err)
	input := jobdb.NewTaskDataOrPanic(map[string]any{"value": 1})
	_, err = first.DoTask(jobdb.RunPolicy{}, "completed", input)
	require.NoError(t, err)
	deadline := first.(*timeoutJobContext).deadline
	waitTimeoutRepro(t, deadline.Add(10*time.Millisecond))
	ctx.cursor = 0
	recovered, err := withDurableExecutionTimeout(ctx, 30*time.Millisecond, "scope")
	require.NoError(t, err)
	require.Equal(t, deadline, recovered.(*timeoutJobContext).deadline)
	_, err = recovered.DoTask(jobdb.RunPolicy{}, "completed", input)
	require.NoError(t, err, "cached work remains replayable after expiry")
	require.Equal(t, *ctx.policies[0].TotalTimeout, *ctx.policies[1].TotalTimeout, "recovery must not shrink or refresh the task budget")
	_, err = recovered.DoTask(jobdb.RunPolicy{}, "unfinished", input)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var timeout *jobdb.TimeoutError
	require.ErrorAs(t, err, &timeout)
	require.False(t, timeout.Payload.Retryable)
	require.Equal(t, 1, ctx.live)
}

func TestNestedTimeoutScopesShareOneTaskAdmission(t *testing.T) {
	ctx := &timeoutReplayContext{t: t}
	parent, err := withDurableExecutionTimeout(ctx, time.Minute, "parent")
	require.NoError(t, err)
	// Inline recipes introduce another thinpack forwarding layer.
	child, err := withDurableExecutionTimeout(newThinPackForwardingJobContext(parent), 2*time.Minute, "child")
	require.NoError(t, err)
	_, err = child.DoTask(jobdb.RunPolicy{}, "work", jobdb.NewTaskDataOrPanic(map[string]any{}))
	require.NoError(t, err)
	require.Len(t, ctx.entries, 4, "two scope checkpoints, one admission, one actual task")
	require.Len(t, ctx.policies, 1)
	requireCapturedTimeoutBetween(t, ctx.policies[0], 59*time.Second, time.Minute)
}

func TestExpiredClampIsNotUnlimited(t *testing.T) {
	policy := clampRunPolicyToDeadline(jobdb.RunPolicy{}, time.Now().Add(-time.Second))
	require.Equal(t, time.Nanosecond, time.Duration(*policy.TotalTimeout))
	zero := jobdb.Duration(0)
	policy = clampRunPolicyToDeadline(jobdb.RunPolicy{TotalTimeout: &zero}, time.Now().Add(time.Second))
	require.Greater(t, time.Duration(*policy.TotalTimeout), time.Duration(0))
	require.LessOrEqual(t, time.Duration(*policy.TotalTimeout), time.Second)
}
