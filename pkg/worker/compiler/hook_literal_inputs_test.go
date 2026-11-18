package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

const literalHookRequest = "Explain 50% and literal {{ example }} and ${{ not_a_reference }}."

const literalHookDefinition = `id: check
input_schema:
 request: {type: string, required: true}
 candidate: {type: string, required: true}
 label: {type: string, default_value: selected-default}
inputs:
 request: '${{ inputs.request }}'
 candidate: '${{ inputs.candidate }}'
 label: '${{ inputs.label }}'
sequence:
 - id: body
   op: workspace_probe
   const: true
outputs:
 request: '${{ inputs.request }}'
 candidate: '${{ inputs.candidate }}'
 label: '${{ inputs.label }}'
`

func literalHookRecipe(workspace, bindings string) string {
	return `id: literal-hook
input_schema:
 request: {type: string, required: true}
inputs: {request: '${{ inputs.request }}'}
` + workspace + `sequence:
 - id: hook
   const: true
   include:
    recipe: cell://.c2/hooks/check.yaml
    if_missing:
     outputs:
      request: '${{ inputs.request }}'
      candidate: '${{ inputs.candidate }}'
      label: absent
   inputs: ` + bindings + `
 - id: continued
   op: workspace_probe
   const: true
outputs:
 request: '${{ sequence.hook.outputs.request }}'
 candidate: '${{ sequence.hook.outputs.candidate }}'
 label: '${{ sequence.hook.outputs.label }}'
 continued: '${{ sequence.continued.outputs.head }}'
`
}

const literalHookBindings = `{request: '${{ inputs.request }}', candidate: '${{ context.git.hash }}'}`

// Supply the request as submission data, never as authored template source.
func submitLiteralHook(t *testing.T, rt jobdb.WorkflowRuntime, repo, ref, body, request string) jobdb.JobKey {
	t.Helper()
	ctx := celltest.Context(context.Background())
	celltest.Create(t, rt, "hooks", "test", celltest.Repo(repo))
	data := jobdb.NewTaskDataOrPanic(workflowctl.StartJob{
		TenantId: "hooks", RecipeName: "literal-hook", Cell: "test", CellRepo: celltest.Repo(repo), GitRef: ref,
		Inputs: map[string]any{"request": request},
		JobContext: contextual.JobContext{
			Workflow: contextual.WorkflowContext{CellName: "test"},
			GitBase:  contextual.GitBaseContext{BaseRepo: repo, BaseRef: ref},
		},
	}, jobdb.NewArtifactFromBytes("literal-hook"+starter.RecipeArtifactSuffix, []byte(body)))
	h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{
		TenantId: "hooks", JobType: "recipe", Cell: "test", CellRepo: celltest.Repo(repo), Data: data,
		RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}},
	}})
	require.NoError(t, err)
	return h.JobKey
}

func TestHookLiteralInputs(t *testing.T) {
	for _, mode := range []string{"implicit-floating", "explicit-floating", "fixed-commit"} {
		for _, present := range []bool{false, true} {
			for _, payload := range []struct{ name, value string }{
				{"go-template", "{{ example }}"},
				{"cel-template", "${{ not_a_reference }}"},
				{"combined", literalHookRequest},
			} {
				t.Run(fmt.Sprintf("%s/present=%t/%s", mode, present, payload.name), func(t *testing.T) {
					repo := filepath.Join(t.TempDir(), "repo")
					head := workspaceRepo(t, repo, "source")
					label := "absent"
					if present {
						head = commitHookFile(t, repo, ".c2/hooks/check.yaml", literalHookDefinition)
						label = "selected-default"
					}
					ref, workspace := "main", ""
					if mode == "fixed-commit" {
						ref = head
					}
					if mode == "explicit-floating" {
						workspace = "workspace: {cell: test, ref: main}\n"
					}
					ctx, eng, rt, _ := hookTestEngine(t, workspaceTestWorker(t))
					key := submitLiteralHook(t, rt, repo, ref, literalHookRecipe(workspace, literalHookBindings), payload.value)
					require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, time.Minute, key, eng))
					result, err := swfutil.JobResult(ctx, eng, key)
					require.NoError(t, err)
					var out map[string]any
					require.NoError(t, json.Unmarshal(result.GetDataOrPanic(), &out))
					require.Equal(t, map[string]any{"request": payload.value, "candidate": head, "label": label, "continued": head}, recipeBusinessOutputs(t, out))

					// Completed execution must replay with no source access or new effects.
					before, err := eng.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key})
					require.NoError(t, err)
					require.NoError(t, os.RemoveAll(repo))
					replayed, err := eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
					require.NoError(t, err)
					var replayedOut map[string]any
					require.NoError(t, json.Unmarshal(replayed.GetDataOrPanic(), &replayedOut))
					require.Equal(t, recipeBusinessOutputs(t, out), recipeBusinessOutputs(t, replayedOut))
					after, err := eng.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key})
					require.NoError(t, err)
					require.Equal(t, before.Execution, after.Execution)
				})
			}
		}
	}
}

func TestHookLiteralInputsRetainSelectedCandidate(t *testing.T) {
	for _, workspace := range []string{"", "workspace: {cell: test, ref: main}\n"} {
		t.Run(fmt.Sprintf("explicit=%t", workspace != ""), func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			head := commitHookFile(t, repo, ".c2/hooks/check.yaml", literalHookDefinition)
			ws := workspaceTestWorker(t)
			for i, w := range ws.TaskWorkers {
				if w.Name() == hookResolveTaskType {
					ws.TaskWorkers[i] = advanceAfterHookLoad{w, t, repo}
				}
			}
			ctx, eng, rt, _ := hookTestEngine(t, ws)
			key := submitLiteralHook(t, rt, repo, "main", literalHookRecipe(workspace, literalHookBindings), literalHookRequest)
			require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, time.Minute, key, eng))
			result, err := swfutil.JobResult(ctx, eng, key)
			require.NoError(t, err)
			var out map[string]any
			require.NoError(t, json.Unmarshal(result.GetDataOrPanic(), &out))
			require.Equal(t, literalHookRequest, out["request"])
			require.Equal(t, head, out["candidate"])
			require.Equal(t, "selected-default", out["label"])
			require.NotEqual(t, head, out["continued"], "later read-only work must keep following the branch")
		})
	}
}

func TestHookLiteralInputsRejectInvalidBindings(t *testing.T) {
	for _, tc := range []struct{ name, bindings, errorText string }{
		{"wrong-type", `{request: 7, candidate: '${{ context.git.hash }}'}`, "sequence inputs do not match schema"},
		{"missing-required", `{candidate: '${{ context.git.hash }}'}`, "sequence inputs do not match schema"},
		{"invalid-expression", `{request: '${{ not_a_reference }}', candidate: '${{ context.git.hash }}'}`, "not_a_reference"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			commitHookFile(t, repo, ".c2/hooks/check.yaml", literalHookDefinition)
			ctx, eng, rt, _ := hookTestEngine(t, workspaceTestWorker(t))
			key := submitLiteralHook(t, rt, repo, "main", literalHookRecipe("", tc.bindings), literalHookRequest)
			require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, time.Minute, key, eng))
			_, err := swfutil.JobResult(ctx, eng, key)
			require.ErrorContains(t, err, tc.errorText)
			run, err := eng.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key})
			require.NoError(t, err)
			for _, entry := range run.Execution {
				require.NotContains(t, entry.Record.ID, `scope:"op:body"`, "invalid inputs must fail before the hook body")
				require.NotContains(t, entry.Record.ID, `scope:"op:continued"`)
			}
		})
	}
}

func TestHookLiteralInputsResume(t *testing.T) {
	for _, phase := range []string{"pin", "load"} {
		for _, present := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/present=%t", phase, present), func(t *testing.T) {
				repo := filepath.Join(t.TempDir(), "repo")
				head := workspaceRepo(t, repo, "source")
				label := "absent"
				if present {
					head = commitHookFile(t, repo, ".c2/hooks/check.yaml", literalHookDefinition)
					label = "selected-default"
				}
				workspaceTestWorker(t)
				rt := newTimeoutReproRuntime(t, "sqlite")
				key := submitLiteralHook(t, rt, repo, "main", literalHookRecipe("", literalHookBindings), literalHookRequest)
				fault := &gitOutcomeInterruption{WorkflowRuntime: rt, match: ":" + phase + `"`}
				_, err := runGitLifecycle(t, fault, key)
				require.Error(t, err)
				require.True(t, fault.fired)
				advanced := commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: changed\nsequence: []\noutputs: {request: wrong, candidate: wrong, label: wrong}\n")
				result, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				require.Equal(t, map[string]any{"request": literalHookRequest, "candidate": head, "label": label, "continued": advanced}, recipeBusinessOutputs(t, gitLifecycleOutput(t, result)))

				ctx := celltest.Context(context.Background())
				run, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true})
				require.NoError(t, err)
				decisions := 0
				for _, entry := range run.Execution {
					if entry.Record.Kind == "result" && strings.Contains(entry.Record.ID, `task:"hook:`) {
						decision := decodeHookDecision(t, jobdb.NewTaskDataOrPanic(json.RawMessage(entry.Record.Output.Data)))
						require.Equal(t, head, decision.Candidate)
						require.Equal(t, head, decision.Source.ResolvedCommit)
						if strings.Contains(entry.Record.ID, `:load"`) {
							require.Equal(t, !present, decision.Absent)
						}
						decisions++
					}
				}
				require.Equal(t, 2, decisions)
			})
		}
	}
}
