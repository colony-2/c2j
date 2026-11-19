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
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/starter"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestImplicitWorkspaceHookKeepsExactCommit(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "source")
	initial := commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: check\nsequence: []\noutputs: {status: original}\n")
	out, _ := runWorkspaceRecipe(t, workspaceTestWorker(t), fmt.Sprintf(`id: workspace-test
sequence:
 - id: hook
   include: cell://.c2/hooks/check.yaml
 - id: advance
   op: workspace_probe
   inputs: {advance: %q}
 - id: inspect
   op: workspace_probe
outputs: {head: '${{ sequence.inspect.outputs.head }}'}
`, repo), contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "repo"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: initial}})
	advanced, err := common.GetCommitHash(context.Background(), repo, "HEAD")
	require.NoError(t, err)
	require.NotEqual(t, initial, advanced)
	require.Equal(t, initial, out["head"])
}

func TestImplicitWorkspaceHookObservationResumeAndRestart(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, present := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/present=%t", backend, present), func(t *testing.T) {
				ctx := celltest.Context(context.Background())
				repo := filepath.Join(t.TempDir(), "repo")
				initial := workspaceRepo(t, repo, "source")
				status := "absent"
				if present {
					status = "original"
					initial = commitHookFile(t, repo, ".c2/hooks/check.yaml", `id: check
sequence:
 - id: read
   op: workspace_probe
   const: true
outputs: {status: original}
`)
				}
				workspaceTestWorker(t)
				rt := newTimeoutReproRuntime(t, backend)
				key := submitGitLifecycle(t, rt, repo, `id: git-lifecycle
sequence:
 - id: hook
   include:
     recipe: cell://.c2/hooks/check.yaml
     if_missing: {outputs: {status: absent}}
 - id: plan
   op: workspace_probe
   const: true
 - id: write
   op: workspace_probe
   inputs: {write: candidate}
 - id: inspect
   op: workspace_probe
   inputs: {read: candidate}
outputs:
 status: '${{ sequence.hook.outputs.status }}'
 plan: '${{ sequence.plan.outputs.head }}'
 base: '${{ sequence.write.outputs.head }}'
 candidate: '${{ sequence.inspect.outputs.head }}'
 text: '${{ sequence.inspect.outputs.text }}'
`)
				// Lose the worker between selecting the hook source and loading it.
				fault := &gitOutcomeInterruption{ConsumerRuntime: rt, match: `:pin"`}
				_, err := runGitLifecycle(t, fault, key)
				require.Error(t, err)
				require.True(t, fault.fired)
				advanced := commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: changed\nsequence: []\noutputs: {status: changed}\n")
				out, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				first := gitLifecycleOutput(t, out)
				require.Equal(t, status, first["status"], "hook definition/absence stays at the recorded source")
				require.Equal(t, advanced, first["plan"], "source selection must not pin the next task")
				require.Equal(t, advanced, first["base"])
				require.NotEqual(t, advanced, first["candidate"])
				require.Equal(t, "test data", first["text"])

				history, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true, IncludeArtifacts: true})
				require.NoError(t, err)
				scopes := map[string]string{}
				hookResults := 0
				for _, entry := range history.Execution {
					r := entry.Record
					if r.Kind == "scope" {
						for _, node := range []string{"write", "inspect"} {
							if strings.HasSuffix(r.ID, `scope:"op:`+node+`"#1`) {
								scopes[node] = r.ID
							}
						}
					}
					if r.Kind != "result" || r.Output == nil {
						continue
					}
					if strings.Contains(r.ID, `task:"hook:`) {
						hookResults++
						result := decodeHookDecision(t, jobdb.NewTaskDataOrPanic(json.RawMessage(r.Output.Data)))
						require.Equal(t, initial, result.Candidate)
						require.Equal(t, initial, result.Source.ResolvedCommit)
						pin := strings.Contains(r.ID, `:pin"`)
						require.Equal(t, pin, result.PreserveFloatingWorkspace)
						require.Equal(t, !pin && !present, result.Absent)
						for _, artifact := range r.Output.Artifacts {
							require.NotEqual(t, gitstate.ThinPackArtifactName, artifact.Name)
						}
					}
					if strings.Contains(r.ID, `scope:"op:plan"`) {
						var input workerops.ActivityInvocationRequest
						require.NoError(t, json.Unmarshal(r.Input.Data, &input))
						require.Empty(t, input.GitTaskContext.PersistHash)
						require.Empty(t, input.GitTaskContext.ParentHash)
						require.Equal(t, "main", input.GitTaskContext.ParentRef)
					}
				}
				require.Equal(t, 2, hookResults)
				require.Len(t, scopes, 2)
				require.NoError(t, runGit(repo, "git", "commit", "--allow-empty", "-m", "advance again"))
				latest, err := common.GetCommitHash(ctx, repo, "HEAD")
				require.NoError(t, err)
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{})).BuildEngine()
				require.NoError(t, err)
				submitter := jobdbschema.WorkflowEngine{Engine: engine, Registry: rt.ConsumerRuntime.(jobdb.JobSchemaRegistry)}
				for _, node := range []string{"inspect", "write"} {
					restarted, err := starter.RestartRecipeJob(ctx, submitter, key, scopes[node], nil, starter.RestartOptions{Goal: "new", RequestID: "hook-restart-" + node})
					require.NoError(t, err)
					out, err := runGitLifecycle(t, rt, restarted.Key())
					require.NoError(t, err)
					result := gitLifecycleOutput(t, out)
					require.Equal(t, status, result["status"])
					require.Equal(t, advanced, result["plan"], "completed observations remain historical")
					if node == "inspect" {
						require.Equal(t, first["candidate"], result["candidate"], "retained writes stay pinned")
					} else {
						require.Equal(t, latest, result["base"], "discarding the first write restores floating behavior")
					}
				}
				require.NoError(t, os.Rename(repo, repo+"-offline"))
				replayed, err := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
				require.NoError(t, err)
				require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
			})
		}
	}
}
