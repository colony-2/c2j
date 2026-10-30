package recipetest

import (
	"context"
	"strings"
	"testing"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/stretchr/testify/require"
)

func TestValueArtifactPassthrough(t *testing.T) {
	withRegisteredCoreOps(t, commandop.GetOp())
	target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `id: value-passthrough
sequence:
 - id: consume
   op: command_execution
   artifacts:
    context.txt:
     kind: value
     format: text
     value: '${{ jq(0, "\"x\" * 524288") }}'
   inputs:
    run: 'wc -c < "${{ context.environment.op.inbox }}/context.txt"'
outputs:
 size: '${{ sequence.consume.outputs.stdout }}'
`}
	testCase := Case{ID: "values", Type: "recipe_case", Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "passthrough"}}}}}
	resp := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "test", target, testCase, ExecutionOptions{ArtifactMode: "inline"})
	require.Equal(t, "passed", resp.Status, resp.FailureReason)
	require.IsType(t, "", resp.Outputs["size"])
	// BSD wc pads the count with spaces; GNU wc does not.
	require.Equal(t, "524288", strings.TrimSpace(resp.Outputs["size"].(string)))
}
