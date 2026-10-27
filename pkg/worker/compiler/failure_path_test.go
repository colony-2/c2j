package compiler

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRecipeFailureFormatsOriginAndPreservesCause(t *testing.T) {
	cause := errors.New("command execution failed: exit status 1")
	failure := &recipe.RuntimeFailure{
		Message: cause.Error(),
		Node:    recipe.FailureNode{Path: "root/failed/command_execution"},
	}
	err := newRecipeFailureError(failure, cause)
	// Outer scopes must retain the original path and message, even when the
	// underlying error has additional context.
	err = newRecipeFailureError(failure, fmt.Errorf("outer scope: %w", err))
	require.EqualError(t, err, "[root.failed.command_execution] command execution failed: exit status 1")
	require.ErrorIs(t, err, cause)
	var app *jobdb.AppError
	require.ErrorAs(t, err, &app)
	raw, marshalErr := json.Marshal(app)
	require.NoError(t, marshalErr)
	var restored jobdb.AppError
	require.NoError(t, json.Unmarshal(raw, &restored))
	restoredFailure, ok := failureFromError(&restored)
	require.True(t, ok)
	require.Equal(t, failure, restoredFailure)
	require.EqualError(t, newRecipeFailureError(restoredFailure, &restored), err.Error())
	require.Equal(t, cause.Error(), restoredFailure.Message, "catch expressions get the original message")
}

func TestNestedFailurePathSurvivesExecutionAndReplay(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"sqlite", "remote"} {
		for _, tc := range []struct {
			name    string
			yaml    string
			path    string
			message string
			calls   int
		}{
			{"sequence", `
id: root
sequence:
  - id: failed
    sequence:
      - op: scope_probe
        inputs: {name: work}
`, "root/failed/scope_probe", "probe failed", 1},
			{"state", `
id: root
state:
  initial: failed
  states:
    failed:
      sequence:
        - id: command
          op: scope_probe
          inputs: {name: work}
`, "root/failed/sequence/command", "probe failed", 1},
			{"rewritten", `
id: root
sequence:
  - id: failed
    catch:
      - when: 'failure.code == "probe"'
        fail:
          message: "deployment failed"
    sequence:
      - id: command
        op: scope_probe
        inputs: {name: work}
`, "root/failed/command", "deployment failed", 1},
			{"before-task", `
id: root
sequence:
  - id: failed
    sequence:
      - id: command
        op: scope_probe
        vars:
          invalid: "${{ inputs.missing }}"
`, "root/failed/command", "", 0},
		} {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				rec, err := recipe.LoadRecipeFromString([]byte(tc.yaml))
				require.NoError(t, err)
				rt := newTimeoutReproRuntime(t, backend)
				_, git := GenerateTestContext()
				task := &scopeProbeTask{calls: map[string]int{}, failOnce: "work", git: git}
				worker := probeWorker(t, *rec)
				key := probeSubmit(t, rt, worker)
				out := probeRun(t, rt, key, worker, task)
				require.Equal(t, jobworkflow.JobRunFailed, out.Status)
				failure, ok := failureFromError(out.JobError)
				require.True(t, ok, "%v", out.JobError)
				require.Equal(t, tc.path, failure.Node.Path)
				if tc.message != "" {
					require.Equal(t, tc.message, failure.Message)
				} else {
					require.Contains(t, failure.Message, "failed to resolve op vars")
				}
				require.EqualError(t, out.JobError, newRecipeFailureError(failure, nil).Error())
				replayErr := probeReplay(t, rt, key, worker)
				require.EqualError(t, replayErr, out.JobError.Error())
				require.Equal(t, tc.calls, task.calls["work"])
			})
		}
	}
}
