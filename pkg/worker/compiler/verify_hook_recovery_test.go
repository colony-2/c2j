package compiler

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/swfutil"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Execute the repository hook through the native compiler and JobDB recovery.
// Commands are external completions, not shell executions or recipe-test mocks.
// Virtual downtime exceeds every former state budget without a wall-clock wait.
func TestVerifyHookDelayedRecovery(t *testing.T) {
	op, err := coreops.NewOp().WithType("command_execution").WithAcceptsArtifacts(true).
		AddStep("run", coreops.NewNoTaskStep[map[string]any, map[string]any]()).Build()
	require.NoError(t, err)
	withRegisteredOps(t, op.(coreops.RegisterableOp))
	source, err := os.ReadFile("../../../.c2/recipes/hooks/build/verify.yaml")
	require.NoError(t, err)
	for _, mode := range []string{"success", "command-timeout", "command-failure", "missing-evidence", "legacy-state-timeout"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rec, err := recipe.LoadRecipeFromString(source)
				require.NoError(t, err)
				machine := rec.RecipeImpl.(*recipe.RecipeState)
				require.Zero(t, machine.Timeout)
				states := []string{"check_environment", "check_modules", "test_integration", "check_skills", "summarize"}
				budgets := []string{"2m", "5m", "120m", "5m", "2m"}
				logs := []string{"environment.log", "modules.log", "tests.log", "skills.log", "summary.md"}
				for i, name := range states {
					node := machine.States.States[name].NodeImpl.(*recipe.NodeOp)
					require.Zero(t, node.Timeout, name)
					require.Equal(t, budgets[i], node.Inputs["timeout"], name)
				}
				if mode == "legacy-state-timeout" {
					// Control: the old five-minute state rejects a successful result on recovery.
					machine.States.States["check_modules"].NodeImpl.(*recipe.NodeOp).Timeout = recipe.Duration(5 * time.Minute)
				}
				rt := toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))
				job, git := GenerateTestContext()
				worker := jobworkflow.ExecutionWorker{JobType: "verify-recovery", Execute: func(exec jobworkflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
					out, artifacts, err := ExecuteRecipe(catchWorkflowContext(exec), *rec, map[string]any{
						"api_version": "c2.build-hook/v1", "request": "fixture", "target_directory": ".",
						"candidate": "fixture-head", "base": "fixture-base", "documents": map[string]any{},
					}, job, git)
					if err != nil {
						return nil, err
					}
					return jobdb.NewTaskData(out, artifacts...)
				}}
				key := probeSubmit(t, rt, worker)
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, err)
				for i, name := range states {
					out := probeRun(t, rt, key, worker)
					if mode == "legacy-state-timeout" && i == 2 {
						require.Equal(t, jobworkflow.JobRunFailed, out.Status)
						var timeout jobdb.TimeoutError
						require.ErrorAs(t, out.JobError, &timeout)
						require.Equal(t, jobdb.TimeoutScopeTotal, timeout.Payload.Scope)
						t.Logf("control reproduced state expiry after accepted module command: %v", out.JobError)
						return
					}
					require.Equal(t, jobworkflow.JobRunSuspended, out.Status, "%s: %v", name, out.JobError)
					pending, err := engine.GetWaitingTask(celltest.Context(context.Background()), key)
					require.NoError(t, err)
					data, err := pending.Data()
					require.NoError(t, err)
					raw, err := data.GetData()
					require.NoError(t, err)
					var request workerops.ActivityInvocationRequest
					require.NoError(t, json.Unmarshal(raw, &request))
					require.Equal(t, budgets[i], request.Input["timeout"], name)
					if name == "summarize" {
						env := request.Input["env"].(map[string]any)
						want := "passed"
						if mode != "success" {
							want = "failed"
						}
						require.Equal(t, want, env["VERDICT"])
					}
					result := map[string]any{"success": true, "exit_code": 0, "timed_out": false, "error_message": "", "stdout": "Recorded check result"}
					if name == "check_modules" && (mode == "command-timeout" || mode == "command-failure") {
						result["success"], result["exit_code"] = false, -1
						result["timed_out"] = mode == "command-timeout"
					}
					envelope, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{OpOutput: result, GitResult: git})
					require.NoError(t, err)
					var artifacts []jobdb.Artifact
					if mode != "missing-evidence" || name != "check_modules" {
						artifacts = append(artifacts, jobdb.NewArtifactFromBytes(logs[i], []byte("recorded fixture evidence\n")))
					}
					time.Sleep(23 * time.Second)
					require.NoError(t, pending.Finish(celltest.Context(context.Background()), jobdb.NewTaskDataOrPanic(envelope, artifacts...)))
					before, err := rt.ListChapters(celltest.Context(context.Background()), jobdb.ListChaptersRequest{JobKey: key})
					require.NoError(t, err)
					budget, err := time.ParseDuration(budgets[i])
					require.NoError(t, err)
					if mode != "legacy-state-timeout" || name == "check_modules" {
						time.Sleep(budget + 18*time.Minute)
					}
					after, err := rt.ListChapters(celltest.Context(context.Background()), jobdb.ListChaptersRequest{JobKey: key})
					require.NoError(t, err)
					require.Equal(t, before, after, "downtime must preserve accepted result")
				}
				out := probeRun(t, rt, key, worker)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				raw, err := out.Output.GetData()
				require.NoError(t, err)
				var result map[string]any
				require.NoError(t, json.Unmarshal(raw, &result))
				want := "passed"
				if mode != "success" {
					want = "failed"
				}
				require.Equal(t, want, result["status"])
				run, err := jobdb.GetJobRun(celltest.Context(context.Background()), rt, jobdb.GetJobRunRequest{JobKey: key})
				require.NoError(t, err)
				tasks, err := swfutil.ExecutionTasks(run)
				require.NoError(t, err)
				require.Len(t, tasks, 5, "recovery must reuse all five accepted commands")
				require.NoError(t, probeReplay(t, rt, key, worker))
				t.Logf("five accepted commands recovered beyond former state deadlines; verdict=%s; replay passed", want)
			})
		})
	}
}
