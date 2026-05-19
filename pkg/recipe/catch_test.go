package recipe

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatchParsesOnRecipeAndNodeMetadata(t *testing.T) {
	registerTestOp()

	raw := `
version: "1.0"
id: root
catch:
  - id: root_fallback
    when: failure.kind == "task_error"
    continue:
      outputs:
        recovered: true
state:
  initial: work
  states:
    work:
      op: echo
      inputs: {message: hi}
      catch:
        - id: route_to_review
          when: true
          to: review
          payload:
            message: ${{ failure.message }}
      transitions:
        - to: done
          when: "true"
    review:
      sequence:
        - op: echo
          id: child
          inputs: {message: review}
          catch:
            - when: false
              continue:
                outputs: {output: skipped}
      outputs:
        output: ${{ sequence.child.outputs.output }}
    done:
      op: echo
      inputs: {message: done}
outputs:
  result: ${{ states.done.outputs.output }}
`
	rec, err := LoadRecipeFromString([]byte(raw))
	require.NoError(t, err)

	root := rec.RecipeImpl.(*RecipeState)
	require.Len(t, root.NodeMetadata.Catch, 1)
	require.Equal(t, "root_fallback", root.NodeMetadata.Catch[0].ID)
	require.NotNil(t, root.NodeMetadata.Catch[0].Continue)

	work := root.States.States["work"]
	require.Len(t, work.GetMetadata().Catch, 1)
	require.Equal(t, "route_to_review", work.GetMetadata().Catch[0].ID)
	require.Equal(t, "review", work.GetMetadata().Catch[0].To)
	require.Equal(t, "", work.GetMetadata().Catch[0].When.String(), "boolean true normalizes to always-match")

	reviewSeq := root.States.States["review"].Node.NodeImpl.(*NodeSequence)
	child := reviewSeq.Sequence[0].NodeImpl.(*NodeOp)
	require.Len(t, child.Catch, 1)
	require.Equal(t, "false", child.Catch[0].When.String())
}

func TestCatchParsingRejectsInvalidActionCombinations(t *testing.T) {
	registerTestOp()

	_, err := LoadRecipeFromString([]byte(`
version: "1.0"
op: echo
inputs: {message: hi}
catch:
  - to: review
    continue:
      outputs: {ok: true}
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one action")
}

func TestCatchSchemaContainsCatchAndRejectsMultipleActions(t *testing.T) {
	registerTestOp()

	schema, err := GenerateSchemaString()
	require.NoError(t, err)
	require.Contains(t, schema, `"catch"`)
	require.Contains(t, schema, `"CatchClause"`)

	err = Validate(`
version: "1.0"
op: echo
inputs: {message: hi}
catch:
  - to: review
    continue:
      outputs: {ok: true}
`)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "oneOf") || strings.Contains(err.Error(), "valid"))
}
