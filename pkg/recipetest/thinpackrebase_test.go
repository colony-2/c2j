package recipetest

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/git/squashrebasemerge"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	coreops "github.com/colony-2/c2j/pkg/ops"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/stretchr/testify/require"
)

// Keep the child-merge regression in c2j's suite; it must not depend on the
// recipes repository or mocked Git operations.
func TestRuntimeChildMergeThenThinpackRefresh(t *testing.T) {
	withRegisteredCoreOps(t, append(recipeops.GetOps(), commandop.GetOp(), thinpackrebase.GetOp(), squashrebasemerge.GetOp())...)
	child := `id: child
version: "1"
sequence:
 - id: write
   op: command_execution
   inputs:
     run: "printf child > mandate"
 - id: head
   op: command_execution
   inputs:
     run: 'printf "%s" "$(git rev-parse HEAD)"'
 - id: merge
   op: squashrebasemerge
   inputs:
     local_hash: '${{ sequence.head.outputs.stdout }}'
     upstream_repo: '${{ context.git.repo }}'
     upstream_branch: '${{ context.git.ref }}'
     commit_message: Child mandate
     rebase: false
outputs:
 merged_hash: '${{ sequence.merge.outputs.merged_hash }}'
`
	target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `id: parent
version: "1"
sequence:
 - id: before
   op: command_execution
   inputs:
     run: 'test ! -f mandate && printf missing'
 - id: child
   op: recipe.run_and_get_result
   inputs:
     name: child
     cell_name: root
     git_ref: '${{ context.git.ref }}'
 - id: refresh
   op: thinpackrebase
   inputs:
     target_base_hash: '${{ sequence.child.outputs.outputs.merged_hash }}'
 - id: after
   op: command_execution
   inputs:
     env:
       BASE: '${{ context.git.execution.base_hash }}'
       HEAD: '${{ context.git.execution.head_hash }}'
     run: 'test "$BASE" = "$HEAD" && cat mandate'
outputs:
 before: '${{ sequence.before.outputs.stdout }}'
 after: '${{ sequence.after.outputs.stdout }}'
 target: '${{ sequence.child.outputs.outputs.merged_hash }}'
 refreshed: '${{ sequence.refresh.outputs.new_base_hash }}'
`}
	c := Case{ID: "child-refresh", Type: "integration_case", Runtime: &RuntimeCase{Cell: "root", Cells: map[string]CellFixture{"root": {Files: map[string]string{".c2/recipes/child.yaml": child, "README.md": "root"}}}}, Mocks: Mocks{Ops: []OpMock{{Repeat: true, Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "passthrough"}}}}}
	result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "refresh-test", target, c, ExecutionOptions{})
	require.Equal(t, "passed", result.Status, result.FailureReason)
	require.Equal(t, "missing", result.Outputs["before"])
	require.Equal(t, "child", result.Outputs["after"])
	require.NotEmpty(t, result.Outputs["refreshed"])
	require.Equal(t, result.Outputs["target"], result.Outputs["refreshed"])
	count := 0
	for _, call := range result.Runtime.Calls {
		if call.Op == "thinpackrebase" {
			count++
		}
	}
	require.Equal(t, 1, count, "success must actually execute refresh")
}
