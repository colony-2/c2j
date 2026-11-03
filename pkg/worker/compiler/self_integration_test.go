package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestSelfDiscoveryFromCommandAndExtension(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "c2j")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../../cmd/c2j")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	for _, backend := range []string{"sqlite", "remote"} {
		for _, extension := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/extension=%t", backend, extension), func(t *testing.T) {
				repo := filepath.Join(t.TempDir(), "repo")
				workspaceRepo(t, repo, "source")
				other := t.TempDir()
				require.NoError(t, os.MkdirAll(filepath.Join(other, ".c2"), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(other, ".c2", "config.yaml"), []byte("self:\n  repo: github.com/other/checkout\n"), 0600))
				captures := t.TempDir()
				run := fmt.Sprintf(`cd %q; n=1; test ! -f %q || n=2; %q self --json > %q/attempt-$n.json; cat %q/attempt-$n.json; test "$n" = 2`, other, filepath.Join(captures, "attempt-1.json"), binary, captures, captures)
				workspaceTestWorker(t, commandop.GetOp(), extops.GetExecutionOp())
				op := "command_execution"
				inputs := fmt.Sprintf("   inputs:\n     run: %q\n     timeout: 30s\n", run)
				if extension {
					commitHookFile(t, repo, "op/op.yaml", fmt.Sprintf("name: inspect\nshell: sh\ntimeout: 30s\nrun: %q\ninput_schema: {type: object}\noutput_schema: {type: object}\n", run))
					op = "./op"
					inputs = ""
				}
				// Denied submit must not disable self. Granted secret names are inspected
				// without consulting a provider; this fixture has no secret provider.
				recipe := fmt.Sprintf(`id: self-discovery
version: '1'
input_schema: {}
sequence:
 - id: probe
   op: %s
   perms: {jobdb: {jobs: {submit: false}, secrets: {resolve: [model]}}}
   retry: {maximum_attempts: 2, initial_interval: 1ms}
   catch:
    - when: false
      continue: {outputs: {unused: true}}
%soutputs: {result: '${{ sequence.probe.outputs }}'}
`, op, inputs)
				rt := newTimeoutReproRuntime(t, backend)
				key := submitGitLifecycle(t, rt, repo, recipe)
				out, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				type report struct {
					selfinfo.Info
					Repo string `json:"repo"`
				}
				attempts := []report{}
				for n := 1; n <= 2; n++ {
					raw, err := os.ReadFile(filepath.Join(captures, fmt.Sprintf("attempt-%d.json", n)))
					require.NoError(t, err)
					var report report
					require.NoError(t, json.Unmarshal(raw, &report))
					attempts = append(attempts, report)
					require.Equal(t, key.JobId, selfinfo.Value(report.Execution.JobID))
					require.Equal(t, key.TenantId, selfinfo.Value(report.Execution.TenantID))
					require.Equal(t, "probe", selfinfo.Value(report.Execution.NodeID))
					require.Equal(t, 1, *report.Execution.AttemptNumber, "recipe op retries create a fresh task under the next scope attempt")
					require.Equal(t, "runtime_helper", report.Execution.Source)
					require.Equal(t, "github.com/other/checkout", report.Repo)
					require.NotEqual(t, report.Repo, selfinfo.Value(report.Workspace.Repo))
					require.Equal(t, "active", report.Lease.Status)
					require.NotNil(t, report.Lease.OperationDeadline)
					require.Equal(t, "denied", report.Capabilities["jobs.submit"].Permission)
					require.Equal(t, "granted", report.Capabilities["secrets.resolve"].Permission)
					require.Equal(t, []string{"model"}, report.Capabilities["secrets.resolve"].Names)
					require.Nil(t, report.Lease.Delegation, "self must not issue credentials")
					require.NotNil(t, report.Workspace.Paths)
				}
				require.NotEmpty(t, selfinfo.Value(attempts[0].Execution.TaskID))
				require.NotEqual(t, selfinfo.Value(attempts[0].Execution.TaskID), selfinfo.Value(attempts[1].Execution.TaskID))
				require.NotEqual(t, *attempts[0].Execution.ResultOrdinal, *attempts[1].Execution.ResultOrdinal)
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{})).BuildEngine()
				require.NoError(t, err)
				// A replay cannot rely on the live broker or the original repository.
				require.NoError(t, os.Rename(repo, repo+"-offline"))
				replayed, err := engine.ReplayJobRun(context.Background(), jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
				require.NoError(t, err)
				require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
			})
		}
	}
}
