package compiler

import (
	"context"
	"fmt"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
)

func commitHookFile(t *testing.T, repo, p, body string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(repo, p)), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, p), []byte(body), 0644))
	require.NoError(t, runGit(repo, "git", "add", "."))
	require.NoError(t, runGit(repo, "git", "commit", "-m", "hook"))
	hash, err := common.GetCommitHash(context.Background(), repo, "HEAD")
	require.NoError(t, err)
	return hash
}
func TestHookReadsCandidateAndPinsReadOnlyExecution(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "A")
	ws := workspaceTestWorker(t)
	commitHookFile(t, repo, ".c2/hooks/leaf.yaml", `id: leaf
sequence:
 - id: read
   op: workspace_probe
   inputs: {read: identity}
outputs: {text: "{{ sequence.read.outputs.text }}", head: "{{ sequence.read.outputs.head }}"}
`)
	head := commitHookFile(t, repo, ".c2/hooks/check.yaml", `id: check
input_schema:
 candidate: {type: string, required: true}
inputs: {candidate: "{{ inputs.candidate }}"}
sequence:
 - id: leaf
   include: ./leaf.yaml
outputs:
 text: "{{ sequence.leaf.outputs.text }}"
 head: "{{ sequence.leaf.outputs.head }}"
 candidate: "{{ inputs.candidate }}"
`)
	out, _ := runWorkspaceRecipe(t, ws, fmt.Sprintf(`id: workspace-test
sequence:
 - id: check
   include: cell://.c2/hooks/check.yaml
   inputs: {candidate: "{{ context.git.hash }}"}
   expect_outputs:
     type: object
     required: [text, head, candidate]
 - id: advance
   op: workspace_probe
   inputs: {advance: %q}
 - id: after
   op: workspace_probe
outputs:
 text: "{{ sequence.check.outputs.text }}"
 candidate: "{{ sequence.check.outputs.candidate }}"
 hook_head: "{{ sequence.check.outputs.head }}"
 after: "{{ sequence.after.outputs.head }}"
`, repo), contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}})
	require.Equal(t, "A", out["text"])
	require.Equal(t, head, out["candidate"])
	require.Equal(t, head, out["hook_head"])
	require.Equal(t, head, out["after"])
}

func TestHookAbsentPinsCandidate(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	head := workspaceRepo(t, repo, "A")
	ws := workspaceTestWorker(t)
	out, _ := runWorkspaceRecipe(t, ws, fmt.Sprintf(`id: workspace-test
sequence:
 - id: check
   include:
     recipe: cell://.c2/hooks/missing.yaml
     if_missing: {outputs: {decision: continue}}
   expect_outputs: {type: object, required: [decision], properties: {decision: {enum: [continue]}}}
 - id: advance
   op: workspace_probe
   inputs: {advance: %q}
 - id: after
   op: workspace_probe
outputs: {decision: "{{ sequence.check.outputs.decision }}", after: "{{ sequence.after.outputs.head }}"}
`, repo), contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}})
	require.Equal(t, "continue", out["decision"])
	require.Equal(t, head, out["after"])
}

func TestHookExpansionIsDeferred(t *testing.T) {
	r, err := recipe.LoadRecipeFromString([]byte("id: parent\nsequence:\n - id: hook\n   include: cell://.c2/hooks/check.yaml\n"))
	require.NoError(t, err)
	expanded, err := ResolveInlineRecipes(context.Background(), *r, InlineResolutionOptions{})
	require.NoError(t, err)
	require.IsType(t, &recipe.NodeInclude{}, expanded.Recipe.RecipeImpl.(*recipe.RecipeSequence).Sequence[0].NodeImpl)
	_, err = ResolveInlineRecipes(context.Background(), recipe.Recipe{RecipeImpl: &recipe.RecipeSequence{SequenceData: recipe.SequenceData{Sequence: []recipe.Node{{NodeImpl: &recipe.NodeInclude{Include: recipe.IncludeData{Recipe: "cell://../escape"}}}}}}}, InlineResolutionOptions{})
	require.Error(t, err)
}

func TestHookOutputContract(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"}, "properties": map[string]any{"decision": map[string]any{"enum": []any{"continue"}}}}
	require.NoError(t, validateExpectedOutputs(schema, map[string]any{"decision": "continue"}))
	require.Error(t, validateExpectedOutputs(schema, map[string]any{"decision": "skip"}))
	require.Error(t, validateExpectedOutputs(map[string]any{"$ref": "https://example.com/schema"}, map[string]any{}))
}

func TestHookSeesUnpushedEditsOnStateRevisit(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "A")
	leaf := ".c2/hooks/leaf.yaml"
	commitHookFile(t, repo, leaf, "id: leaf\nsequence: []\noutputs: {decision: revise}\n")
	original := commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: check\nsequence:\n - id: leaf\n   include: ./leaf.yaml\noutputs: {decision: '{{ sequence.leaf.outputs.decision }}'}\n")
	edit := coreops.NewActivityMappedOpV2[map[string]any, map[string]any](coreops.OpMetadata{Type: "hook_edit"}, func(deps coreops.OpDependencies, _ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{}, os.WriteFile(filepath.Join(deps.WorktreePath(), leaf), []byte("id: leaf\nsequence: []\noutputs: {decision: continue}\n"), 0644)
	})
	ws := workspaceTestWorker(t, edit)
	out, _ := runWorkspaceRecipe(t, ws, `id: workspace-test
state:
 initial: check
 states:
  check:
   include: cell://.c2/hooks/check.yaml
   expect_outputs: {type: object, required: [decision], properties: {decision: {enum: [revise, continue]}}}
   transitions:
    - {to: edit, when: "states.check.outputs.decision == 'revise'"}
    - {to: done}
  edit:
   op: hook_edit
   transitions: [{to: check}]
  done:
   op: workspace_probe
outputs:
 decision: '{{ states.check.outputs.decision }}'
 hash: '{{ states.done.outputs.head }}'
`, contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}})
	require.Equal(t, "continue", out["decision"])
	require.NotEqual(t, original, out["hash"])
	source, err := common.GetCommitHash(context.Background(), repo, "HEAD")
	require.NoError(t, err)
	require.Equal(t, original, source, "edits must remain unpushed")
}

func TestHookPinsAtArrivalAfterFloatingReadOnlyStep(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "A")
	original := commitHookFile(t, repo, ".c2/hooks/check.yaml", `id: check
inputs: {candidate: '{{ inputs.candidate }}'}
sequence:
 - id: read
   op: workspace_probe
   inputs: {read: moved}
outputs: {text: '{{ sequence.read.outputs.text }}', candidate: '{{ inputs.candidate }}', head: '{{ sequence.read.outputs.head }}'}
`)
	out, _ := runWorkspaceRecipe(t, workspaceTestWorker(t), fmt.Sprintf(`id: workspace-test
sequence:
 - id: before
   op: workspace_probe
   inputs: {advance: %q}
 - id: hook
   include: cell://.c2/hooks/check.yaml
   inputs: {candidate: '{{ context.git.hash }}'}
outputs:
 candidate: '{{ sequence.hook.outputs.candidate }}'
 head: '{{ sequence.hook.outputs.head }}'
 text: '{{ sequence.hook.outputs.text }}'
`, repo), contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}})
	require.Equal(t, "new tip", out["text"])
	require.Equal(t, out["head"], out["candidate"])
	require.NotEqual(t, original, out["candidate"])
}

func TestHookRetryReusesDefinition(t *testing.T) {
	for _, state := range []bool{false, true} {
		t.Run(fmt.Sprint("state=", state), func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "A")
			hook := ".c2/hooks/check.yaml"
			commitHookFile(t, repo, hook, "id: check\nsequence:\n - id: probe\n   op: hook_retry_probe\noutputs: {decision: original}\n")
			var calls atomic.Int32
			op := coreops.NewActivityMappedOpV2[map[string]any, map[string]any](coreops.OpMetadata{Type: "hook_retry_probe"}, func(_ coreops.OpDependencies, _ context.Context, _ map[string]any) (map[string]any, error) {
				if calls.Add(1) == 1 {
					commitHookFile(t, repo, hook, "id: check\nsequence: []\noutputs: {decision: changed}\n")
					return nil, fmt.Errorf("retry me")
				}
				return map[string]any{}, nil
			})
			body := `id: workspace-test
sequence:
 - id: hook
   include: cell://.c2/hooks/check.yaml
   retry: {maximum_attempts: 2, initial_interval: 1ms}
outputs: {decision: '{{ sequence.hook.outputs.decision }}'}
`
			if state {
				body = `id: workspace-test
state:
 initial: hook
 states:
  hook:
   include: cell://.c2/hooks/check.yaml
   retry: {maximum_attempts: 2, initial_interval: 1ms}
outputs: {decision: '{{ states.hook.outputs.decision }}'}
`
			}
			out, _ := runWorkspaceRecipe(t, workspaceTestWorker(t, op), body, contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}})
			require.Equal(t, "original", out["decision"])
			require.Equal(t, int32(2), calls.Load())
		})
	}
}

func TestHookStaticValidationUsesContractWithoutSource(t *testing.T) {
	for _, body := range []string{`id: parent
sequence:
 - id: hook
   include: cell://.c2/hooks/check.yaml
   expect_outputs: {type: object, properties: {decision: {enum: [continue]}}}
outputs: {decision: '{{ sequence.hook.outputs.decision }}'}
`, `id: parent
state:
 initial: hook
 states:
  hook:
   include: cell://.c2/hooks/check.yaml
   expect_outputs: {type: object, properties: {decision: {enum: [continue]}}}
   transitions: [{to: done, when: "states.hook.outputs.decision == 'continue'"}]
  done:
   sequence: []
outputs: {decision: '{{ states.hook.outputs.decision }}'}
`} {
		rec, err := recipe.LoadRecipeFromString([]byte(body))
		require.NoError(t, err)
		require.NoError(t, validateCatchRecipe(t, *rec))
	}
}
