package compiler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type toolTimeoutStep struct {
	taskType string
	elapsed  time.Duration
	budget   time.Duration // Zero means no total timeout.
	output   jobdb.TaskData
}

type toolTimeoutEntry struct {
	taskType string
	input    string
	policy   jobdb.RunPolicy
	output   jobdb.TaskData
	err      error
}

// Timeout checkpoints are the compiler's clock boundary. Supplying their
// timestamps exercises the real compiler without running workers, Git, or timers.
type toolTimeoutContext struct {
	policyCaptureJobContext
	t       *testing.T
	now     time.Time
	steps   []toolTimeoutStep
	live    int
	history []toolTimeoutEntry
	replay  bool
	cursor  int
}

func (c *toolTimeoutContext) DoTask(policy jobdb.RunPolicy, taskType string, input jobdb.TaskData) (jobdb.TaskData, error) {
	raw, err := input.GetData()
	require.NoError(c.t, err)
	if c.replay {
		require.Less(c.t, c.cursor, len(c.history), "replay must not schedule new work")
		entry := c.history[c.cursor]
		c.cursor++
		require.Equal(c.t, entry.taskType, taskType)
		require.JSONEq(c.t, entry.input, string(raw))
		require.Equal(c.t, entry.policy, policy, "replay must preserve the exact task budget")
		return entry.output, entry.err
	}
	var output jobdb.TaskData
	if taskType == TimeoutCheckpointTaskType {
		output = jobdb.NewTaskDataOrPanic(timeoutCheckpointOutput{At: c.now})
	} else {
		require.Less(c.t, c.live, len(c.steps), "unexpected task %s", taskType)
		step := c.steps[c.live]
		c.live++
		require.Equal(c.t, step.taskType, taskType)
		if step.budget == 0 {
			require.Nil(c.t, policy.TotalTimeout)
		} else {
			require.NotNil(c.t, policy.TotalTimeout)
			require.Equal(c.t, step.budget, time.Duration(*policy.TotalTimeout))
		}
		if taskType == workerops.ToolSetupTaskType {
			require.NotNil(c.t, policy.InvocationTimeout)
			require.Equal(c.t, workerops.SetupTimeout, time.Duration(*policy.InvocationTimeout))
		}
		c.now = c.now.Add(step.elapsed)
		output = step.output
		if step.budget > 0 && step.elapsed >= step.budget {
			output, err = nil, context.DeadlineExceeded
		}
	}
	c.history = append(c.history, toolTimeoutEntry{taskType, string(raw), policy, output, err})
	return output, err
}

func TestToolSetupTimeoutAccounting(t *testing.T) {
	const opType = "tool_timeout_probe"
	withRegisteredOps(t, newTimeoutTestOp(t, opType, 0))
	job, git := GenerateTestContext()
	success := newActivityOutputTaskData(t, git)
	envelope, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{SetupRequired: true})
	require.NoError(t, err)
	missing := jobdb.NewTaskDataOrPanic(envelope)
	setup := func(duration time.Duration) jobdb.TaskData {
		return jobdb.NewTaskDataOrPanic(workerops.ToolSetupResult{Duration: duration})
	}
	for _, tc := range []struct {
		name    string
		parent  string
		steps   []toolTimeoutStep
		expires bool
	}{
		{
			name: "initial setup precedes op budget",
			steps: []toolTimeoutStep{
				{workerops.ToolSetupTaskType, 20 * time.Second, 0, setup(20 * time.Second)},
				{opType + ":run", time.Second, 10 * time.Second, success},
			},
		},
		{
			name: "recovery preserves remaining op budget",
			steps: []toolTimeoutStep{
				{workerops.ToolSetupTaskType, 20 * time.Second, 0, setup(20 * time.Second)},
				{opType + ":run", 2 * time.Second, 10 * time.Second, missing},
				{workerops.ToolSetupTaskType, 30 * time.Second, 0, setup(30 * time.Second)},
				{opType + ":run", time.Second, 8 * time.Second, success},
			},
		},
		{
			name: "recovery does not reset spent op budget",
			steps: []toolTimeoutStep{
				{workerops.ToolSetupTaskType, 20 * time.Second, 0, setup(20 * time.Second)},
				{opType + ":run", 2 * time.Second, 10 * time.Second, missing},
				{workerops.ToolSetupTaskType, 30 * time.Second, 0, setup(30 * time.Second)},
				{opType + ":run", 9 * time.Second, 8 * time.Second, success},
			},
			expires: true,
		},
		{
			name:   "parent budget includes initial and recovery setup",
			parent: "timeout: 53s\n",
			steps: []toolTimeoutStep{
				{workerops.ToolSetupTaskType, 20 * time.Second, 53 * time.Second, setup(20 * time.Second)},
				{opType + ":run", 2 * time.Second, 10 * time.Second, missing},
				{workerops.ToolSetupTaskType, 30 * time.Second, 31 * time.Second, setup(30 * time.Second)},
				{opType + ":run", 0, time.Second, success},
			},
		},
		{
			name:   "parent can expire during recovery setup",
			parent: "timeout: 51s\n",
			steps: []toolTimeoutStep{
				{workerops.ToolSetupTaskType, 20 * time.Second, 51 * time.Second, setup(20 * time.Second)},
				{opType + ":run", 2 * time.Second, 10 * time.Second, missing},
				{workerops.ToolSetupTaskType, 30 * time.Second, 29 * time.Second, setup(30 * time.Second)},
			},
			expires: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, err := recipe.LoadRecipeFromString([]byte(fmt.Sprintf(`id: tool-timeout
%sexecution: {packages: [uv:tool==1]}
sequence:
  - id: work
    op: %s
    timeout: 10s
`, tc.parent, opType)))
			require.NoError(t, err)
			clock := &toolTimeoutContext{t: t, now: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), steps: tc.steps}
			check := func() {
				_, _, err := ExecuteRecipe(newWorkflowContext(clock), *rec, nil, job, git)
				if tc.expires {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				} else {
					require.NoError(t, err)
				}
			}
			check()
			require.Equal(t, len(tc.steps), clock.live)
			// Replaying years later uses recorded times and setup durations.
			clock.replay = true
			clock.now = clock.now.AddDate(10, 0, 0)
			check()
			require.Equal(t, len(clock.history), clock.cursor)
			require.Equal(t, len(tc.steps), clock.live)
		})
	}
}
