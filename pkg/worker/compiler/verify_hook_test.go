package compiler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

// Native YAML mocks cannot inject typed cancellation errors. Exercise the actual
// hook through the compiler with the same scripted task context as catch tests.
func TestVerifyHookCancellation(t *testing.T) {
	withRegisteredOps(t, commandop.GetOp())
	data, err := os.ReadFile("../../../.c2/recipes/hooks/build/verify.yaml")
	require.NoError(t, err)
	r, err := recipe.LoadRecipeFromString(data)
	require.NoError(t, err)
	for stop := 0; stop < 5; stop++ {
		t.Run([]string{"environment", "modules", "integration", "skills", "summary"}[stop], func(t *testing.T) {
			jobCtx, gitCtx := GenerateTestContext()
			script := &catchScriptedJobContext{jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "verify-cancel"}}
			for i := 0; i < stop; i++ {
				script.results = append(script.results, catchTaskResult{out: catchOutput(t, gitCtx, map[string]interface{}{
					"success": true, "exit_code": 0, "timed_out": false, "stdout": "", "error_message": "",
				})})
			}
			script.results = append(script.results, catchTaskResult{err: context.Canceled})
			inputs := map[string]interface{}{"api_version": "c2.build-hook/v1", "request": "verify", "target_directory": ".", "candidate": "candidate", "base": "base", "documents": map[string]interface{}{}}
			out, _, runErr := ExecuteRecipe(catchWorkflowContext(script), *r, inputs, jobCtx, gitCtx)
			require.ErrorIs(t, runErr, context.Canceled)
			require.Empty(t, out)
			require.Equal(t, stop+1, script.calls, "cancellation must not run later checks or reporting")
		})
	}
}

func TestVerifyHookReport(t *testing.T) {
	data, err := os.ReadFile("../../../.c2/recipes/hooks/build/verify.yaml")
	require.NoError(t, err)
	r, err := recipe.LoadRecipeFromString(data)
	require.NoError(t, err)
	state := r.RecipeImpl.(*recipe.RecipeState)
	node := state.States.States["summarize"].Node.NodeImpl.(*recipe.NodeOp)
	root := t.TempDir()
	inbox, outbox := filepath.Join(root, "inbox"), filepath.Join(root, "outbox")
	require.NoError(t, os.Mkdir(inbox, 0700))
	require.NoError(t, os.Mkdir(outbox, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "environment.log"), []byte("Go 1.26.1\nNode v24.0.0\nOptional Docker absent\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "tests.log"), []byte(strings.Repeat("large output\n", 10000)+"--- SKIP: TestDocker (0.00s)\n"), 0600))
	cmd := exec.Command("bash", "-c", node.Inputs["run"].(string))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "INBOX="+inbox, "OUTBOX="+outbox, "CANDIDATE=candidate; $(touch never-execute)", "BASE=base", "VERDICT=failed")
	for i := 0; i < 4; i++ {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RESULT_%d=failed", i), fmt.Sprintf("DETAIL_%d={\"success\":false}", i))
	}
	stdout, err := cmd.CombinedOutput()
	require.NoError(t, err, string(stdout))
	require.LessOrEqual(t, len(stdout), 8192)
	report, err := os.ReadFile(filepath.Join(outbox, "summary.md"))
	require.NoError(t, err)
	for _, text := range []string{"candidate; $(touch never-execute)", "Go 1.26.1", "Node v24.0.0", "--- SKIP: TestDocker", "Evidence unavailable: modules.log", "Evidence unavailable: skills.log", "does not certify the GitHub matrix"} {
		require.Contains(t, string(report), text)
	}
	_, err = os.Stat(filepath.Join(root, "never-execute"))
	require.True(t, os.IsNotExist(err), "candidate input must be treated as data")
}

func TestVerifyHookSubmissionEnvironmentAndGate(t *testing.T) {
	data, err := os.ReadFile("../../../.c2/recipes/hooks/build/verify.yaml")
	require.NoError(t, err)
	r, err := recipe.LoadRecipeFromString(data)
	require.NoError(t, err)
	state := r.RecipeImpl.(*recipe.RecipeState)
	node := state.States.States["test_integration"].Node.NodeImpl.(*recipe.NodeOp)
	for _, failGate := range []bool{false, true} {
		t.Run(fmt.Sprint(failGate), func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			outbox := filepath.Join(root, "outbox")
			require.NoError(t, os.Mkdir(bin, 0700))
			require.NoError(t, os.Mkdir(outbox, 0700))
			// Run the hook's actual shell with a recording Go command. It rejects leaked
			// live routing and proves a failed process gate prevents the broad suite.
			mock := `#!/bin/bash
set -eu
for variable in ${!C2J_CURRENT_@} ${!C2J_CHILD_JOB_@} ${!C2J_WORKSPACE_@}; do exit 91; done
test -z "${C2J_JOBDB+x}${C2J_TENANT_ID+x}${C2J_OBJECT_OUTBOX+x}${C2J_TEST_SUBMIT_BINARY+x}"
test "$GOWORK" = off
printf '%s\n' "$*" >> "$COMMANDS"
if [[ "$*" == *TestBrokerOnlyCLI* ]] && [ "$FAIL_GATE" = true ]; then exit 42; fi
`
			require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(mock), 0700))
			commands := filepath.Join(root, "commands")
			cmd := exec.Command("bash", "-c", node.Inputs["run"].(string))
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "OUTBOX="+outbox, "COMMANDS="+commands, fmt.Sprintf("FAIL_GATE=%t", failGate), "C2J_CURRENT_JOB_ID=live", "C2J_CHILD_JOB_TOKEN=live", "C2J_WORKSPACE_ROOT=live", "C2J_JOBDB=embed:///", "C2J_TENANT_ID=live", "C2J_OBJECT_OUTBOX=live", "C2J_TEST_SUBMIT_BINARY=/stale/c2j")
			out, err := cmd.CombinedOutput()
			if failGate {
				require.Error(t, err)
				require.Equal(t, 42, cmd.ProcessState.ExitCode())
			} else {
				require.NoError(t, err, string(out))
			}
			raw, err := os.ReadFile(commands)
			require.NoError(t, err)
			require.Contains(t, string(raw), "-run ^TestBrokerOnlyCLI")
			if failGate {
				require.NotContains(t, string(raw), "-tags=integration")
			} else {
				require.Contains(t, string(raw), "-run ^TestSubmission")
				require.Contains(t, string(raw), "-tags=integration")
			}
		})
	}
}
