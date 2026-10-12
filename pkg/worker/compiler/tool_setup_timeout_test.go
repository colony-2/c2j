package compiler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/recipe"
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

		c.now = c.now.Add(step.elapsed)
		output = step.output
		if step.budget > 0 && step.elapsed >= step.budget {
			output, err = nil, context.DeadlineExceeded
		}
	}
	c.history = append(c.history, toolTimeoutEntry{taskType, string(raw), policy, output, err})
	return output, err
}

func TestPreparationSharesTaskTimeout(t *testing.T) {
	const opType = "tool_timeout_probe"
	withRegisteredOps(t, newTimeoutTestOp(t, opType, 0))
	job, git := GenerateTestContext()
	for _, elapsed := range []time.Duration{time.Second, 11 * time.Second} {
		t.Run(elapsed.String(), func(t *testing.T) {
			rec, err := recipe.LoadRecipeFromString([]byte(fmt.Sprintf("id: shared-timeout\nexecution: {packages: [uv:tool==1]}\nsequence:\n  - op: %s\n    timeout: 10s\n", opType)))
			require.NoError(t, err)
			clock := &toolTimeoutContext{t: t, now: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), steps: []toolTimeoutStep{{opType + ":run", elapsed, 10 * time.Second, newActivityOutputTaskData(t, git)}}}
			check := func() {
				_, _, err := ExecuteRecipe(newWorkflowContext(clock), *rec, nil, job, git)
				if elapsed >= 10*time.Second {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				} else {
					require.NoError(t, err)
				}
			}
			check()
			require.Equal(t, 1, clock.live)
			clock.replay = true
			clock.now = clock.now.AddDate(10, 0, 0)
			check()
			require.Equal(t, len(clock.history), clock.cursor)
		})
	}
}
