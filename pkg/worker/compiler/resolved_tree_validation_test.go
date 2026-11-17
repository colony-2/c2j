package compiler

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestResolvedTreeCompilerAndWorkerRejectMissingOrStaleConfirmations(t *testing.T) {
	for _, mode := range []string{"missing-base", "missing-persist", "missing-target", "stale-ancestral-base", "stale-tree", "stale-candidate", "null", "empty", "const", "whole-object-template"} {
		t.Run(mode, func(t *testing.T) {
			repo, base, target := resolvedFixture(t)
			calls := 0
			op := resolvedCountedOp{thinpackrebase.GetRebaseOp(), func(d coreops.OpDependencies, ctx context.Context, input map[string]any, native func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error)) (map[string]any, error) {
				calls++
				return native(d, ctx, input)
			}}
			workspaceTestWorker(t, append(resolvedFixtureOps(t, target, false), op)...)
			body := resolvedRecipe("rebase", target, "direct")
			switch mode {
			case "missing-base":
				body = strings.Replace(body, "    base_hash: '${{ sequence.prepare.outputs.base }}'\n", "", 1)
			case "missing-persist":
				body = strings.Replace(body, "    persist_hash: '${{ sequence.prepare.outputs.head }}'\n", "", 1)
			case "missing-target":
				body = strings.Replace(body, fmt.Sprintf("    target_base_hash: %s\n", target), "", 1)
			case "stale-ancestral-base":
				body = strings.Replace(body, "base_hash: '${{ sequence.prepare.outputs.base }}'", "base_hash: '${{ sequence.prepare.outputs.older_base }}'", 1)
			case "stale-tree":
				body = strings.Replace(body, "tree_hash: '${{ sequence.prepare.outputs.tree }}'", "tree_hash: "+strings.Repeat("a", 40), 1)
			case "stale-candidate":
				body = strings.Replace(body, "persist_hash: '${{ sequence.prepare.outputs.head }}'", "persist_hash: '${{ sequence.prepare.outputs.base }}'", 1)
			case "null":
				body = strings.Replace(body, "resolution: {tree_hash: '${{ sequence.prepare.outputs.tree }}'}", "resolution: null", 1)
			case "empty":
				body = strings.Replace(body, "resolution: {tree_hash: '${{ sequence.prepare.outputs.tree }}'}", "resolution: {}", 1)
			case "const":
				body = strings.Replace(body, " - id: integrate\n", " - id: integrate\n   const: true\n", 1)
			case "whole-object-template":
				body = strings.Replace(body, "resolution: {tree_hash: '${{ sequence.prepare.outputs.tree }}'}", "resolution: '${{ sequence.prepare.outputs.confirmation }}'", 1)
			}
			if mode != "whole-object-template" {
				body = strings.Replace(body, " - id: observe", `   catch:
    - when: true
      continue:
       outputs: {rejected: true, failure: '${{ failure }}'}
 - id: observe`, 1)
			}
			rt := newTimeoutReproRuntime(t, "sqlite")
			key := submitGitLifecycle(t, rt, repo, body)
			out, err := runGitLifecycle(t, rt, key)
			require.NoError(t, err)
			result := gitLifecycleOutput(t, out)
			restored := result["restored"].(map[string]any)
			if mode == "whole-object-template" {
				require.Equal(t, target, restored["base"])
				require.Equal(t, 1, calls)
				return
			}
			require.Equal(t, true, result["integration"].(map[string]any)["rejected"])
			require.Equal(t, base, restored["base"])
			prepared := result["prepared"].(map[string]any)
			require.Equal(t, prepared["head"], restored["head"])
			require.Equal(t, prepared["tree"], restored["tree"])
			if strings.HasPrefix(mode, "missing-") || mode == "null" || mode == "empty" {
				require.Zero(t, calls, "compiler must reject before task defaults or worker execution")
			}
		})
	}
}

func TestResolvedTreeScopedWorkspaceLeavesEnclosingSnapshotIntact(t *testing.T) {
	repo, base, target := resolvedFixture(t)
	workspaceTestWorker(t, append(resolvedFixtureOps(t, target, false), thinpackrebase.GetRebaseOp())...)
	inner := resolvedRecipe("rebase", target, "direct")
	inner = strings.TrimPrefix(inner, "id: resolved-tree-lifecycle\nversion: \"1\"\n")
	body := fmt.Sprintf(`id: scoped-adoption
sequence:
 - id: enclosing
   op: workspace_probe
   inputs: {write: enclosing}
 - id: before
   op: resolved_observe
 - id: scoped
   workspace: {cell: test, ref: %s}
   %s
 - id: after
   op: resolved_observe
outputs:
 before: '${{ sequence.before.outputs }}'
 scoped: '${{ sequence.scoped.outputs }}'
 after: '${{ sequence.after.outputs }}'
`, base, strings.ReplaceAll(strings.TrimSpace(inner), "\n", "\n   "))
	rt := newTimeoutReproRuntime(t, "sqlite")
	key := submitGitLifecycle(t, rt, repo, body)
	out, err := runGitLifecycle(t, rt, key)
	require.NoError(t, err)
	result := gitLifecycleOutput(t, out)
	before, after := result["before"].(map[string]any), result["after"].(map[string]any)
	for _, field := range []string{"head", "tree", "base", "accepted", "diff"} {
		require.Equal(t, before[field], after[field], field)
	}
	require.Equal(t, base, after["base"])
	adopted := result["scoped"].(map[string]any)["restored"].(map[string]any)
	require.Equal(t, target, adopted["base"])
	require.NotEqual(t, before["head"], adopted["head"])
	require.NotContains(t, adopted["diff"], "enclosing")
}
