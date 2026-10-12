package compiler

import (
	"context"
	"fmt"
	"testing"
	"time"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestSecretSetupExcludesOpBudgetAndReplays(t *testing.T) {
	type input struct {
		Token string `json:"token" secret:"true"`
	}
	const opType = "secret_timeout_probe"
	op := coreops.NewActivityMappedOpV2[input, map[string]any](coreops.OpMetadata{Type: opType}, func(coreops.OpDependencies, context.Context, input) (map[string]any, error) {
		t.Fatal("unexpected live op")
		return nil, nil
	})
	withRegisteredOps(t, op)
	job, git := GenerateTestContext()
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{SecretSetupRequired: true})
	require.NoError(t, err)
	missing := jobdb.NewTaskDataOrPanic(env)
	for _, parent := range []bool{false, true} {
		t.Run(fmt.Sprint(parent), func(t *testing.T) {
			timeout := ""
			budget := time.Duration(0)
			if parent {
				timeout = "timeout: 15s\n"
				budget = 13 * time.Second
			}
			rec, err := recipe.LoadRecipeFromString([]byte("id: secret-timeout\n" + timeout + "sequence:\n  - id: probe\n    op: " + opType + "\n    timeout: 10s\n    inputs: {token: '${{ context.secrets.token }}'}\n"))
			require.NoError(t, err)
			steps := []toolTimeoutStep{
				{opType + ":" + opType, 2 * time.Second, 10 * time.Second, missing},
				{workerops.SecretSetupTaskType, 20 * time.Second, budget, jobdb.NewTaskDataOrPanic(workerops.SecretSetupResult{Duration: 20 * time.Second})},
			}
			if !parent {
				steps = append(steps, toolTimeoutStep{opType + ":" + opType, time.Second, 8 * time.Second, newActivityOutputTaskData(t, git)})
			}
			clock := &toolTimeoutContext{t: t, now: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), steps: steps}
			check := func() {
				_, _, err := ExecuteRecipe(newWorkflowContext(clock), *rec, nil, job, git)
				if parent {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				} else {
					require.NoError(t, err)
				}
			}
			check()
			require.Equal(t, len(steps), clock.live)
			clock.replay = true
			clock.now = clock.now.AddDate(10, 0, 0)
			check()
			require.Equal(t, len(clock.history), clock.cursor)
		})
	}
}
