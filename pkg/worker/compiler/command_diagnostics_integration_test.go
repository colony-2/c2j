package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type commandCaptureInput struct {
	Failure map[string]any `json:"failure"`
	Inbox   string         `json:"inbox"`
}

func TestCommandDiagnosticsNestedCatchResumeAndReplay(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, scenario := range []string{"state", "include", "retry", "timeout"} {
			t.Run(backend+"/"+scenario, func(t *testing.T) {
				repo := filepath.Join(t.TempDir(), "repo")
				workspaceRepo(t, repo, "source")
				// Outside the worktree so this also detects accidental command reexecution.
				counter := filepath.Join(t.TempDir(), "runs")
				run := fmt.Sprintf(`printf 'run\n' >> %q; printf 'checking tools\n'; printf 'Required tool unavailable: go\n' >&2; exit 7`, counter)
				policy, timeout := "", ""
				expectedStdout, expectedStderr := "checking tools\n", "Required tool unavailable: go\n"
				if scenario == "retry" {
					// An inner catch gives this op's retry policy precedence over
					// the enclosing catch; the clause itself never handles failure.
					policy = "   retry: {maximum_attempts: 2, initial_interval: 1ms}\n   catch:\n    - when: false\n      continue: {outputs: {unused: true}}\n"
					// BSD wc pads its count; arithmetic expansion keeps fixture output portable.
					run = fmt.Sprintf(`printf 'run\n' >> %q; n=$(( $(wc -l < %q) )); printf 'attempt %%s\n' "$n"; printf 'error %%s\n' "$n" >&2; exit 7`, counter, counter)
					expectedStdout, expectedStderr = "attempt 2\n", "error 2\n"
				}
				if scenario == "timeout" {
					run = fmt.Sprintf(`printf 'run\n' >> %q; printf 'checking tools\n'; printf 'Required tool unavailable: go\n' >&2; sleep 5`, counter)
					timeout = "     timeout: 200ms\n"
				}
				commitHookFile(t, repo, ".c2/hooks/leaf.yaml", fmt.Sprintf("id: leaf\nversion: '1'\ninput_schema: {}\nsequence:\n - id: failed_command\n   op: command_execution\n%s   inputs:\n     run: %q\n%soutputs: {}\n", policy, run, timeout))
				commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: check\nversion: '1'\ninput_schema: {}\nsequence:\n - id: nested_hook\n   include: ./leaf.yaml\noutputs: {}\n")
				capture := coreops.NewActivityMappedOpV2[commandCaptureInput, map[string]any](coreops.OpMetadata{Type: "command_test_capture", AcceptsArtifacts: true}, func(_ coreops.OpDependencies, _ context.Context, in commandCaptureInput) (map[string]any, error) {
					log, err := os.ReadFile(filepath.Join(in.Inbox, "execution.log"))
					if err != nil {
						return nil, err
					}
					return map[string]any{"failure": in.Failure, "log": string(log)}, nil
				})
				workspaceTestWorker(t, commandop.GetOp(), capture)
				catch := `   catch:
    - when: true
      to: recover
      payload:
        failure: '${{ failure }}'
        diagnostics: '${{ failure.attrs.command.diagnostics }}'
`
				if scenario == "include" {
					catch = `       catch:
        - when: true
          continue:
            outputs:
              failure: '${{ failure }}'
              diagnostics: '${{ failure.attrs.command.diagnostics }}'
   outputs:
     failure: '${{ sequence.hook.outputs.failure }}'
     diagnostics: '${{ sequence.hook.outputs.diagnostics }}'
   transitions:
    - to: recover
      payload:
        failure: '${{ states.check.outputs.failure }}'
        diagnostics: '${{ states.check.outputs.diagnostics }}'
`
				}
				yaml := `id: git-lifecycle
version: '1'
input_schema: {}
state:
 initial: check
 states:
  check:
   sequence:
     - id: hook
       include: cell://.c2/hooks/check.yaml
` + catch + `  recover:
   op: command_test_capture
   artifacts:
     execution.log: '${{ transition.payload.diagnostics["_c2j/execution/failed_command.log"] }}'
   inputs:
     failure: '${{ transition.payload.failure }}'
     inbox: '${{ context.environment.inbox }}'
outputs: {captured: '${{ states.recover.outputs }}'}
`
				rt := newTimeoutReproRuntime(t, backend)
				key := submitGitLifecycle(t, rt, repo, yaml)
				fault := &gitOutcomeInterruption{WorkflowRuntime: rt, match: `task:"command_execution:command_execution"`}
				_, err := runGitLifecycle(t, fault, key)
				require.Error(t, err)
				require.True(t, fault.fired)
				out, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				captured := gitLifecycleOutput(t, out)["captured"].(map[string]any)
				require.Contains(t, captured["log"], strings.TrimSpace(expectedStdout))
				require.Contains(t, captured["log"], strings.TrimSpace(expectedStderr))
				failure := captured["failure"].(map[string]any)
				kind, exit := "task_error", float64(7)
				if scenario == "timeout" {
					kind, exit = "timeout", -1
				}
				require.Equal(t, kind, failure["kind"])
				node := failure["node"].(map[string]any)
				require.Equal(t, "failed_command", node["id"])
				require.Equal(t, "command_execution", node["op"])
				details := failure["attrs"].(map[string]any)["command"].(map[string]any)
				require.Equal(t, float64(2), details["version"])
				require.Equal(t, exit, details["exit_code"])
				require.Equal(t, scenario == "timeout", details["timed_out"])
				require.NotContains(t, details, "diagnostic_errors")
				raw, err := json.Marshal(details["diagnostics"])
				require.NoError(t, err)
				refs := map[string]artifacts.Ref{}
				require.NoError(t, json.Unmarshal(raw, &refs))
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{})).BuildEngine()
				require.NoError(t, err)
				for name, ref := range refs {
					stored, ok := ref.StoredKey()
					require.True(t, ok)
					require.Equal(t, key.JobId, stored.JobId)
					// Read the persisted artifact independently of the consuming op.
					data, err := stored.ToLazyArtifact(engine, key.TenantId).Bytes(celltest.Context(context.Background()))
					require.NoError(t, err)
					require.Equal(t, "_c2j/execution/failed_command.log", name)
					require.Equal(t, captured["log"], string(data))
				}

				if scenario == "retry" {
					history, err := jobdb.GetJobRun(celltest.Context(context.Background()), rt, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true, IncludeArtifacts: true})
					require.NoError(t, err)
					ordinals := map[int64]bool{}
					attemptNumber := 0
					for _, task := range executionTaskRuns(history) {
						if task.TaskType != "command_execution:command_execution" {
							continue
						}
						for _, attempt := range task.Attempts {
							attemptNumber++
							require.NotNil(t, attempt.Output)
							streamCount := 0
							for _, artifact := range attempt.Output.Artifacts {
								if artifact.Name != "_c2j/execution/failed_command.log" {
									continue
								}
								streamCount++
								require.NotNil(t, artifact.Key)
								ordinals[artifact.Key.TaskOrdinal] = true
								data, err := artifact.Key.ToLazyArtifact(engine, key.TenantId).Bytes(celltest.Context(context.Background()))
								require.NoError(t, err)
								require.Contains(t, string(data), fmt.Sprintf("attempt %d", attemptNumber))
								require.Contains(t, string(data), fmt.Sprintf("error %d", attemptNumber))
							}
							require.Equal(t, 1, streamCount)
						}
					}
					require.Equal(t, 2, attemptNumber)
					require.Len(t, ordinals, 2, "failed attempts must never overwrite each other's evidence")
				}
				require.NoError(t, os.Rename(repo, repo+"-offline"))
				replayed, err := engine.ReplayJobRun(celltest.Context(context.Background()), jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
				require.NoError(t, err)
				require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
				runs, err := os.ReadFile(counter)
				require.NoError(t, err)
				expectedRuns := "run\n"
				if scenario == "retry" {
					expectedRuns += "run\n"
				}
				require.Equal(t, expectedRuns, string(runs))
			})
		}
	}
}

func TestExtensionExecutionLogCatchAndReplay(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, scenario := range []string{"success", "schema", "oversized"} {
			t.Run(backend+"/"+scenario, func(t *testing.T) {
				repo := filepath.Join(t.TempDir(), "repo")
				workspaceRepo(t, repo, "source")
				counter := filepath.Join(t.TempDir(), "runs")
				run := fmt.Sprintf(`printf 'run\n' >> %q; printf '{"value":"ok"}'`, counter)
				if scenario == "schema" {
					run = fmt.Sprintf(`printf 'run\n' >> %q; printf '{"value":42}'`, counter)
				}
				if scenario == "oversized" {
					run = fmt.Sprintf(`printf 'run\n' >> %q; printf '{"value":"'; head -c 200000 /dev/zero | tr '\000' x; printf '"}'`, counter)
				}
				commitHookFile(t, repo, "op/op.yaml", fmt.Sprintf("name: check\nshell: sh\nrun: %q\ninput_schema: {type: object}\noutput_schema: {type: object, additionalProperties: false, properties: {value: {type: string}}}\n", run))
				capture := coreops.NewActivityMappedOpV2[commandCaptureInput, map[string]any](coreops.OpMetadata{Type: "extension_log_capture", AcceptsArtifacts: true}, func(_ coreops.OpDependencies, _ context.Context, in commandCaptureInput) (map[string]any, error) {
					raw, err := os.ReadFile(filepath.Join(in.Inbox, "execution.log"))
					if err != nil {
						return nil, err
					}
					// Keep the consumer's result small, including the oversized-result case.
					return map[string]any{"failed": strings.Contains(string(raw), "Operation result: failed:"), "exited_zero": strings.Contains(string(raw), "Result: exited with code 0"), "failure": in.Failure}, nil
				})
				workspaceTestWorker(t, extops.GetExecutionOp(), capture)
				recipe := `id: extension-log
version: '1'
input_schema: {}
sequence:
 - id: check
   op: ./op
   catch:
    - when: true
      continue:
        outputs:
          failure: '${{ failure }}'
          diagnostics: '${{ failure.attrs.extension.diagnostics }}'
 - id: inspect
   op: extension_log_capture
   artifacts:
     execution.log: '${{ sequence.check.artifacts["_c2j/execution/check.log"] }}'
   inputs:
     inbox: '${{ context.environment.op.inbox }}'
outputs:
 result: '${{ sequence.inspect.outputs }}'
`
				if scenario != "success" {
					recipe = strings.Replace(recipe, `sequence.check.artifacts["_c2j/execution/check.log"]`, `sequence.check.outputs.diagnostics["_c2j/execution/check.log"]`, 1)
					recipe = strings.Replace(recipe, "     inbox:", "     failure: '${{ sequence.check.outputs.failure }}'\n     inbox:", 1)
				}
				rt := newTimeoutReproRuntime(t, backend)
				key := submitGitLifecycle(t, rt, repo, recipe)
				out, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				result := gitLifecycleOutput(t, out)["result"].(map[string]any)
				require.Equal(t, scenario != "success", result["failed"])
				require.Equal(t, true, result["exited_zero"])
				if scenario != "success" {
					failure := result["failure"].(map[string]any)
					require.Equal(t, "check", failure["node"].(map[string]any)["id"])
				}
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{})).BuildEngine()
				require.NoError(t, err)
				require.NoError(t, os.Rename(repo, repo+"-offline"))
				replayed, err := engine.ReplayJobRun(celltest.Context(context.Background()), jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
				require.NoError(t, err)
				require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
				runs, err := os.ReadFile(counter)
				require.NoError(t, err)
				require.Equal(t, "run\n", string(runs))
			})
		}
	}
}
