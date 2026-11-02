package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	coreops "github.com/colony-2/c2j/pkg/ops"
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
					run = fmt.Sprintf(`printf 'run\n' >> %q; n=$(wc -l < %q); printf 'attempt %%s\n' "$n"; printf 'error %%s\n' "$n" >&2; exit 7`, counter, counter)
					expectedStdout, expectedStderr = "attempt 2\n", "error 2\n"
				}
				if scenario == "timeout" {
					run = fmt.Sprintf(`printf 'run\n' >> %q; printf 'checking tools\n'; printf 'Required tool unavailable: go\n' >&2; sleep 5`, counter)
					timeout = "     timeout: 200ms\n"
				}
				commitHookFile(t, repo, ".c2/hooks/leaf.yaml", fmt.Sprintf("id: leaf\nversion: '1'\ninput_schema: {}\nsequence:\n - id: failed_command\n   op: command_execution\n%s   inputs:\n     run: %q\n%soutputs: {}\n", policy, run, timeout))
				commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: check\nversion: '1'\ninput_schema: {}\nsequence:\n - id: nested_hook\n   include: ./leaf.yaml\noutputs: {}\n")
				capture := coreops.NewActivityMappedOpV2[commandCaptureInput, map[string]any](coreops.OpMetadata{Type: "command_test_capture", AcceptsArtifacts: true}, func(_ coreops.OpDependencies, _ context.Context, in commandCaptureInput) (map[string]any, error) {
					stdout, err := os.ReadFile(filepath.Join(in.Inbox, "stdout.txt"))
					if err != nil {
						return nil, err
					}
					stderr, err := os.ReadFile(filepath.Join(in.Inbox, "stderr.txt"))
					if err != nil {
						return nil, err
					}
					return map[string]any{"failure": in.Failure, "stdout": string(stdout), "stderr": string(stderr)}, nil
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
     stdout.txt: '${{ transition.payload.diagnostics["_c2j/command/stdout.txt"] }}'
     stderr.txt: '${{ transition.payload.diagnostics["_c2j/command/stderr.txt"] }}'
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
				require.Equal(t, expectedStdout, captured["stdout"])
				require.Equal(t, expectedStderr, captured["stderr"])
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
				require.Equal(t, float64(1), details["version"])
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
					data, err := stored.ToLazyArtifact(engine, key.TenantId).Bytes(context.Background())
					require.NoError(t, err)
					expected := expectedStdout
					if name == commandop.StderrArtifactName {
						expected = expectedStderr
					}
					require.Equal(t, expected, string(data))
				}

				if scenario == "retry" {
					history, err := jobdb.GetJobRun(context.Background(), rt, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true, IncludeArtifacts: true})
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
								if artifact.Name != commandop.StdoutArtifactName && artifact.Name != commandop.StderrArtifactName {
									continue
								}
								streamCount++
								require.NotNil(t, artifact.Key)
								ordinals[artifact.Key.TaskOrdinal] = true
								data, err := artifact.Key.ToLazyArtifact(engine, key.TenantId).Bytes(context.Background())
								require.NoError(t, err)
								prefix := "attempt"
								if artifact.Name == commandop.StderrArtifactName {
									prefix = "error"
								}
								require.Equal(t, fmt.Sprintf("%s %d\n", prefix, attemptNumber), string(data))
							}
							require.Equal(t, 2, streamCount)
						}
					}
					require.Equal(t, 2, attemptNumber)
					require.Len(t, ordinals, 2, "failed attempts must never overwrite each other's evidence")
				}
				require.NoError(t, os.Rename(repo, repo+"-offline"))
				replayed, err := engine.ReplayJobRun(context.Background(), jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
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
