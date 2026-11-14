package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/swfutil"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// A forbidden workspace worker is a boundary probe: no target lookup, checkout
// or credential access can occur past it. Source and parent work still run.
type absentWorkspaceProbe struct {
	jobworkflow.TaskWorker
	calls *atomic.Int32
}

func (w absentWorkspaceProbe) Run(tc jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, data)
}
func (w absentWorkspaceProbe) RunTask(ctx context.Context, tc jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	var req WorkspaceResolutionInput
	if err := json.Unmarshal(data.GetDataOrPanic(), &req); err != nil {
		return nil, err
	}
	if req.Cell == "forbidden" {
		w.calls.Add(1)
		return nil, fmt.Errorf("forbidden execution target reached")
	}
	return coretask.RunLocal(w.TaskWorker, ctx, tc, data)
}
func poisonAbsentWorkspace(ws *jobworkflow.WorkSet, calls *atomic.Int32) {
	for i, w := range ws.TaskWorkers {
		if w.Name() == WorkspaceResolutionTaskType {
			ws.TaskWorkers[i] = absentWorkspaceProbe{w, calls}
		}
	}
}

func absentWorkspaceRecipe(state bool, workspace string, outputs string, extra string) string {
	hook := `include:
 recipe: cell://.c2/hooks/check.yaml
 if_missing:
  outputs:
` + indentAbsent(outputs, 3) + `
` + workspace + `
` + extra
	if state {
		return `id: absent-workspace
state:
 initial: hook
 states:
  hook:
` + indentAbsent(hook, 3) + `
   transitions: [{to: done}]
  done:
   sequence: []
   outputs: {continued: true}
outputs:
 result: '${{ states.hook.outputs }}'
 continued: '${{ states.done.outputs.continued }}'
`
	}
	return `id: absent-workspace
sequence:
 - id: hook
` + indentAbsent(hook, 3) + `
 - id: done
   sequence: []
   outputs: {continued: true}
outputs:
 result: '${{ sequence.hook.outputs }}'
 continued: '${{ sequence.done.outputs.continued }}'
`
}
func indentAbsent(s string, n int) string {
	return strings.Repeat(" ", n) + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n"+strings.Repeat(" ", n))
}

func TestHookAbsentWorkspaceSuppressed(t *testing.T) {
	for _, state := range []bool{false, true} {
		for _, target := range []string{"none", "literal", "unevaluable", "foreign"} {
			t.Run(fmt.Sprintf("state=%t/%s", state, target), func(t *testing.T) {
				root := t.TempDir()
				repo := filepath.Join(root, "source")
				workspaceRepo(t, repo, "A")
				var calls atomic.Int32
				ws := workspaceTestWorker(t)
				poisonAbsentWorkspace(ws, &calls)
				ctx, eng, rt, provider := hookTestEngine(t, ws)
				workspace := "workspace: {cell: forbidden, ref: unreachable}"
				switch target {
				case "none":
					workspace = ""
				case "unevaluable":
					workspace = `workspace: {cell: '${{ inputs.never_bound }}', ref: '${{ inputs.also_missing }}'}`
				}
				text := absentWorkspaceRecipe(state, workspace, `api_version: c2.build-hook/v1
status: skipped
summary: No repository publish hook is configured.
artifact_refs: {}`, `expect_outputs:
 type: object
 required: [api_version, status, summary, artifact_refs]
 additionalProperties: false
 properties:
  api_version: {const: c2.build-hook/v1}
  status: {const: skipped}
  summary: {const: No repository publish hook is configured.}
  artifact_refs: {type: object, maxProperties: 0}`)
				if target == "foreign" {
					b := filepath.Join(root, "foreign")
					workspaceRepo(t, b, "B")
					celltest.Create(t, rt, "hooks", "B", celltest.Repo(b))
					text = "workspace: {cell: B}\n" + text
				}
				job := startHookTest(t, ctx, eng, repo, text)
				result, err := swfutil.JobResult(ctx, eng, job)
				require.NoError(t, err)
				require.JSONEq(t, `{"result":{"api_version":"c2.build-hook/v1","status":"skipped","summary":"No repository publish hook is configured.","artifact_refs":{}},"continued":true}`, string(result.GetDataOrPanic()))
				require.Zero(t, calls.Load())
				require.Empty(t, provider.Calls())
				run, err := eng.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: job, IncludeInputs: true, IncludeOutputs: true})
				require.NoError(t, err)
				count := 0
				for _, task := range executionTaskRuns(run) {
					if task.TaskType == WorkspaceResolutionTaskType {
						count++
						for _, attempt := range task.Attempts {
							require.NotContains(t, string(attempt.Input.Data), "forbidden")
						}
					}
				}
				want := 0
				if target == "foreign" {
					want = 1
				}
				require.Equal(t, want, count, "complete task history must omit the include-local workspace")
				before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: job})
				require.NoError(t, err)
				require.NoError(t, os.RemoveAll(root))
				replayed, err := eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: job, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
				require.NoError(t, err)
				require.JSONEq(t, string(result.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
				after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: job})
				require.NoError(t, err)
				require.Equal(t, before, after)
				require.Zero(t, calls.Load())
				require.Empty(t, provider.Calls())
			})
		}
	}
}

func TestHookAbsentWorkspaceBindings(t *testing.T) {
	for _, state := range []bool{false, true} {
		for _, foreign := range []bool{false, true} {
			t.Run(fmt.Sprintf("state=%t/foreign=%t", state, foreign), func(t *testing.T) {
				repo := filepath.Join(t.TempDir(), "source")
				candidate := workspaceRepo(t, repo, "A")
				ctx, eng, rt, _ := hookTestEngine(t, workspaceTestWorker(t))
				text := absentWorkspaceRecipe(state, `workspace: {cell: '${{ inputs.unbound }}'}`, `value: '${{ inputs["odd-key\"\\"] }}'
candidate: '${{ inputs.candidate }}'
cell: '${{ context.workspace.cell }}'
hash: '${{ context.git.hash }}'
variable: '${{ vars.here }}'`, `inputs:
 'odd-key"\': '${{ context.workspace.cell }}'
 candidate: '${{ context.git.hash }}'
vars: {here: '${{ context.workspace.cell }}'}`)
				cell := "test"
				hash := candidate
				if foreign {
					b := filepath.Join(t.TempDir(), "B")
					workspaceRepo(t, b, "B")
					hash = "" // An untouched floating caller has no accepted Git hash.
					text = strings.Replace(text, "candidate: '${{ context.git.hash }}'", "candidate: "+candidate, 1)
					celltest.Create(t, rt, "hooks", "B", celltest.Repo(b))
					text = "workspace: {cell: B}\n" + text
					cell = "B"
				}
				job := startHookTest(t, ctx, eng, repo, text)
				result, err := swfutil.JobResult(ctx, eng, job)
				require.NoError(t, err)
				var out map[string]any
				require.NoError(t, json.Unmarshal(result.GetDataOrPanic(), &out))
				require.Equal(t, map[string]any{"value": cell, "candidate": candidate, "cell": cell, "hash": hash, "variable": cell}, out["result"])
			})
		}
	}
}

func TestHookAbsentWorkspaceFailureBoundaries(t *testing.T) {
	for _, state := range []bool{false, true} {
		for _, mode := range []string{"required", "malformed", "symlink", "contract", "parent", "empty-sequence", "present", "invalid-schema", "catch-valid", "catch-invalid"} {
			t.Run(fmt.Sprintf("state=%t/%s", state, mode), func(t *testing.T) {
				repo := filepath.Join(t.TempDir(), "repo")
				workspaceRepo(t, repo, "A")
				if mode == "present" {
					commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: check\nsequence: []\noutputs: {status: present}\n")
				}
				if mode == "malformed" {
					commitHookFile(t, repo, ".c2/hooks/check.yaml", "sequence: [")
				}
				if mode == "symlink" {
					require.NoError(t, os.MkdirAll(filepath.Join(repo, ".c2/hooks"), 0755))
					require.NoError(t, os.Symlink("nowhere", filepath.Join(repo, ".c2/hooks/check.yaml")))
					commitHookFile(t, repo, "other", "x")
				}
				extra := `expect_outputs: {type: object, required: [status], properties: {status: {const: skipped}}}`
				if mode == "invalid-schema" {
					extra = "expect_outputs: {type: invalid}"
				}
				outputs := "status: skipped"
				if strings.HasPrefix(mode, "catch") || mode == "contract" {
					outputs = "status: wrong"
				}
				if strings.HasPrefix(mode, "catch") {
					recovered := "skipped"
					if mode == "catch-invalid" {
						recovered = "wrong"
					}
					extra += "\ncatch:\n - continue:\n    outputs: {status: " + recovered + "}"
				}
				text := absentWorkspaceRecipe(state, "workspace: {cell: forbidden}", outputs, extra)
				if mode == "parent" {
					text = "workspace: {cell: forbidden}\n" + text
				}
				// Build the two controls directly to avoid coupling them to indentation.
				if mode == "required" {
					text = "id: required\nsequence:\n - id: hook\n   include: cell://.c2/hooks/check.yaml\n   workspace: {cell: forbidden}\n"
				}
				if mode == "empty-sequence" {
					text = "id: empty\nsequence:\n - id: empty\n   workspace: {cell: forbidden}\n   sequence: []\n"
				}
				var calls atomic.Int32
				ws := workspaceTestWorker(t)
				poisonAbsentWorkspace(ws, &calls)
				ctx, eng, _, _ := hookTestEngine(t, ws)
				job := startHookTest(t, ctx, eng, repo, text)
				_, err := swfutil.JobResult(ctx, eng, job)
				if mode == "catch-valid" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				want := int32(0)
				if mode == "parent" || mode == "empty-sequence" || mode == "present" {
					want = 1
					require.ErrorContains(t, err, "forbidden execution target")
				}
				require.Equal(t, want, calls.Load())
				run, e := eng.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: job})
				require.NoError(t, e)
				if mode == "invalid-schema" {
					for _, task := range executionTaskRuns(run) {
						require.NotEqual(t, hookResolveTaskType, task.TaskType)
					}
				}
				if err != nil {
					for _, entry := range run.Execution {
						require.NotContains(t, entry.Record.ID, `sequence:done`)
					}
				}
			})
		}
	}
}

// Advance upstream only after source selection, so checking the input alone
// cannot hide a checkout of the wrong commit.
type advanceAfterHookLoad struct {
	jobworkflow.TaskWorker
	t    *testing.T
	repo string
}

func (w advanceAfterHookLoad) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func (w advanceAfterHookLoad) RunTask(ctx context.Context, tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	out, err := coretask.RunLocal(w.TaskWorker, ctx, tc, in)
	if err != nil {
		return out, err
	}
	var req workerops.ActivityInvocationRequest
	if err := json.Unmarshal(in.GetDataOrPanic(), &req); err != nil {
		return nil, err
	}
	if req.Input["phase"] == "load" {
		commitHookFile(w.t, w.repo, "upstream-advanced", "B")
	}
	return out, nil
}
func TestHookPresentWorkspaceRetainsSelectedCommit(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "A")
	head := commitHookFile(t, repo, ".c2/hooks/check.yaml", `id: check
inputs: {candidate: '${{ inputs.candidate }}'}
sequence:
 - id: read
   op: workspace_probe
outputs:
 head: '${{ sequence.read.outputs.head }}'
 candidate: '${{ inputs.candidate }}'
 cell: '${{ sequence.read.outputs.cell }}'
`)
	ws := workspaceTestWorker(t)
	for i, w := range ws.TaskWorkers {
		if w.Name() == hookResolveTaskType {
			ws.TaskWorkers[i] = advanceAfterHookLoad{w, t, repo}
		}
	}
	ctx, eng, _, _ := hookTestEngine(t, ws)
	text := fmt.Sprintf(`id: present-workspace
workspace: {cell: test, ref: %q}
sequence:
 - id: before
   op: workspace_probe
 - id: hook
   workspace: {cell: test, ref: %q}
   include:
    recipe: cell://.c2/hooks/check.yaml
    if_missing: {outputs: {head: wrong, candidate: wrong, cell: wrong}}
   inputs: {candidate: '${{ context.git.hash }}'}
 - id: after
   op: workspace_probe
outputs:
 hook: '${{ sequence.hook.outputs }}'
 before: '${{ sequence.before.outputs.scope }}'
 after: '${{ sequence.after.outputs.scope }}'
 after_head: '${{ sequence.after.outputs.head }}'
`, head, head)
	key := startHookTest(t, ctx, eng, repo, text)
	result, err := swfutil.JobResult(ctx, eng, key)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(result.GetDataOrPanic(), &out))
	require.Equal(t, map[string]any{"head": head, "candidate": head, "cell": "test"}, out["hook"])
	require.Equal(t, out["before"], out["after"])
	require.Equal(t, head, out["after_head"])
	tip, err := common.GetCommitHash(testBackground(), repo, "HEAD")
	require.NoError(t, err)
	require.NotEqual(t, head, tip)
}

func TestHookAbsentCurrentSavedChanges(t *testing.T) {
	for _, mode := range []string{"addition", "removal"} {
		t.Run(mode, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "A")
			hook := ".c2/hooks/check.yaml"
			if mode == "removal" {
				commitHookFile(t, repo, hook, "id: check\nsequence: []\noutputs: {status: present}\n")
			}
			edit := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "absent_hook_edit"}, func(d coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
				if mode == "removal" {
					return map[string]any{}, os.Remove(filepath.Join(d.WorktreePath(), hook))
				}
				if err := os.MkdirAll(filepath.Join(d.WorktreePath(), ".c2/hooks"), 0755); err != nil {
					return nil, err
				}
				return map[string]any{}, os.WriteFile(filepath.Join(d.WorktreePath(), hook), []byte("id: check\nsequence: []\noutputs: {status: present}\n"), 0644)
			})
			localWorkspace := ""
			want := "present"
			if mode == "removal" {
				localWorkspace = "   workspace: {cell: forbidden}\n"
				want = "skipped"
			}
			var calls atomic.Int32
			ws := workspaceTestWorker(t, edit)
			poisonAbsentWorkspace(ws, &calls)
			ctx, eng, _, _ := hookTestEngine(t, ws)
			text := `id: saved-hook
source_hooks: from_current
sequence:
 - id: edit
   op: absent_hook_edit
 - id: hook
` + localWorkspace + `   include:
    recipe: cell://.c2/hooks/check.yaml
    if_missing: {outputs: {status: skipped}}
outputs: {status: '${{ sequence.hook.outputs.status }}'}
`
			key := startHookTest(t, ctx, eng, repo, text)
			result, err := swfutil.JobResult(ctx, eng, key)
			require.NoError(t, err)
			require.JSONEq(t, fmt.Sprintf(`{"status":%q}`, want), string(result.GetDataOrPanic()))
			require.Zero(t, calls.Load())
			_, err = os.Stat(filepath.Join(repo, hook))
			require.Equal(t, mode == "addition", os.IsNotExist(err), "source branch was not changed")
		})
	}
}

func TestHookAbsentStateRevisitDiscoversNewHook(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "A")
	edit := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "absent_hook_create"}, func(d coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
		if err := os.MkdirAll(filepath.Join(d.WorktreePath(), ".c2/hooks"), 0755); err != nil {
			return nil, err
		}
		return map[string]any{}, os.WriteFile(filepath.Join(d.WorktreePath(), ".c2/hooks/check.yaml"), []byte("id: check\nsequence: []\noutputs: {status: present}\n"), 0644)
	})
	out, _ := runWorkspaceRecipe(t, workspaceTestWorker(t, edit), `id: workspace-test
state:
 initial: hook
 states:
  hook:
   include:
    recipe: cell://.c2/hooks/check.yaml
    if_missing: {outputs: {status: skipped}}
   transitions:
    - {to: edit, when: "states.hook.outputs.status == 'skipped'"}
    - {to: done}
  edit:
   op: absent_hook_create
   transitions: [{to: hook}]
  done:
   sequence: []
outputs: {status: '${{ states.hook.outputs.status }}'}
`, contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}})
	require.Equal(t, "present", out["status"])
}
