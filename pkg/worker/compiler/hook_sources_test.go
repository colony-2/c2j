package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/swfutil"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestHookSourcesAcrossRebase(t *testing.T) {
	for _, mode := range []string{"", "from_base", "from_base_with_rebase", "from_current"} {
		for _, scoped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/scoped=%t", mode, scoped), func(t *testing.T) {
				repo := filepath.Join(t.TempDir(), "repo")
				workspaceRepo(t, repo, "A")
				leaf := ".c2/hooks/leaf.yaml"
				check := ".c2/hooks/check.yaml"
				leafYAML := "id: leaf\nsequence: []\noutputs: {version: %s}\n"
				checkYAML := `id: check
source_hooks: from_current
sequence:
 - id: leaf
   include: ./leaf.yaml
 - id: nested
   include: cell://.c2/hooks/nested.yaml
 - id: read
   op: workspace_probe
   inputs: {read: job.txt}
outputs:
 version: '{{ sequence.leaf.outputs.version }}'
 nested: '{{ sequence.nested.outputs.version }}'
 marker: %s
 text: '{{ sequence.read.outputs.text }}'
 head: '{{ sequence.read.outputs.head }}'
`
				commitHookFile(t, repo, leaf, fmt.Sprintf(leafYAML, "A"))
				commitHookFile(t, repo, ".c2/hooks/nested.yaml", fmt.Sprintf(leafYAML, "upstream"))
				initial := commitHookFile(t, repo, check, fmt.Sprintf(checkYAML, "upstream"))
				edit := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "edit_hook_candidate"}, func(d coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
					for path, body := range map[string]string{check: fmt.Sprintf(checkYAML, "job"), ".c2/hooks/nested.yaml": fmt.Sprintf(leafYAML, "job"), "job.txt": "candidate changes"} {
						if err := os.WriteFile(filepath.Join(d.WorktreePath(), path), []byte(body), 0644); err != nil {
							return nil, err
						}
					}
					return map[string]any{}, nil
				})
				advance := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "advance_hook_base"}, func(_ coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
					return map[string]any{"hash": commitHookFile(t, repo, leaf, fmt.Sprintf(leafYAML, "B"))}, nil
				})
				config := ""
				if mode != "" {
					config += "source_hooks: " + mode + "\n"
				}
				if scoped {
					config += fmt.Sprintf("workspace: {cell: %q, ref: %q}\n", repo, initial)
				}
				out, _ := runWorkspaceRecipe(t, workspaceTestWorker(t, edit, advance, thinpackrebase.GetRebaseOp()), `id: workspace-test
`+config+`sequence:
 - id: edit
   op: edit_hook_candidate
 - id: before
   include: cell://.c2/hooks/check.yaml
 - id: advance
   op: advance_hook_base
 - id: unchanged
   include: cell://.c2/hooks/check.yaml
 - id: rebase
   op: rebase
   inputs: {target_base_hash: '{{ sequence.advance.outputs.hash }}'}
 - id: after
   include: cell://.c2/hooks/check.yaml
outputs:
 before: '${{ sequence.before.outputs }}'
 unchanged: '${{ sequence.unchanged.outputs }}'
 after: '${{ sequence.after.outputs }}'
 rebased: '{{ sequence.rebase.outputs.new_persist_hash }}'
`, contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}})
				marker := "upstream"
				if mode == "from_current" {
					marker = "job"
				}
				for _, step := range []string{"before", "unchanged", "after"} {
					result := out[step].(map[string]any)
					version := "A"
					if step == "after" && mode != "from_base" {
						version = "B"
					}
					require.Equal(t, version, result["version"], step)
					require.Equal(t, marker, result["marker"], step)
					require.Equal(t, marker, result["nested"], "hook cannot override consuming recipe's source mode")
					require.Equal(t, "candidate changes", result["text"], step)
				}
				require.Equal(t, out["before"], out["unchanged"], "upstream advancement without rebase must not change hook source")
				require.Equal(t, out["rebased"], out["after"].(map[string]any)["head"], "loading upstream hook must preserve candidate")
			})
		}
	}
}

func TestHookSourceFirstInvocationAfterRebaseAndReplay(t *testing.T) {
	for _, mode := range []string{"from_base", "from_base_with_rebase", "from_current"} {
		t.Run(mode, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "A")
			path := ".c2/hooks/check.yaml"
			commitHookFile(t, repo, path, "id: check\nsequence: []\noutputs: {version: A}\n")
			advance := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "advance_hook_base"}, func(_ coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
				return map[string]any{"hash": commitHookFile(t, repo, path, "id: check\nsequence: []\noutputs: {version: B}\n")}, nil
			})
			ctx, eng, _, _ := hookTestEngine(t, workspaceTestWorker(t, advance, thinpackrebase.GetRebaseOp()))
			job := startHookTest(t, ctx, eng, repo, `id: late-hook
source_hooks: `+mode+`
sequence:
 - id: edit
   op: workspace_probe
   inputs: {write: job.txt}
 - id: advance
   op: advance_hook_base
 - id: rebase
   op: rebase
   inputs: {target_base_hash: '{{ sequence.advance.outputs.hash }}'}
 - id: hook
   include: cell://.c2/hooks/check.yaml
outputs: {version: '{{ sequence.hook.outputs.version }}'}
`)
			result, err := swfutil.JobResult(ctx, eng, job)
			require.NoError(t, err)
			var out map[string]any
			require.NoError(t, json.Unmarshal(result.GetDataOrPanic(), &out))
			want := "B"
			if mode == "from_base" {
				want = "A"
			}
			require.Equal(t, want, out["version"])
			require.NoError(t, os.RemoveAll(repo))
			_, err = eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: job, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err, "replay must use the recorded base and hook definition without accessing source")
		})
	}
}

func TestHookSourceAbsenceUsesSelectedCommit(t *testing.T) {
	for _, mode := range []string{"from_base", "from_base_with_rebase", "from_current"} {
		t.Run(mode, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "A")
			add := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "add_candidate_hook"}, func(d coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
				if err := os.MkdirAll(filepath.Join(d.WorktreePath(), ".c2/hooks"), 0755); err != nil {
					return nil, err
				}
				return map[string]any{}, os.WriteFile(filepath.Join(d.WorktreePath(), ".c2/hooks/new.yaml"), []byte("id: new\nsequence: []\noutputs: {version: job}\n"), 0644)
			})
			out, _ := runWorkspaceRecipe(t, workspaceTestWorker(t, add), `id: workspace-test
source_hooks: `+mode+`
sequence:
 - id: add
   op: add_candidate_hook
 - id: hook
   include:
     recipe: cell://.c2/hooks/new.yaml
     if_missing: {outputs: {version: absent}}
outputs: {version: '{{ sequence.hook.outputs.version }}'}
`, contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}})
			want := "absent"
			if mode == "from_current" {
				want = "job"
			}
			require.Equal(t, want, out["version"])
		})
	}
}
