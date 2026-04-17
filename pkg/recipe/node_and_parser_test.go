package recipe

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"
)

type testOpIn struct {
	Message string `yaml:"message" json:"message"`
}
type testOpOut struct {
	Output string `yaml:"output" json:"output"`
}

func registerTestOp() {
	ops.Clear()
	testOp := ops.NewActivityMappedOpV2[testOpIn, testOpOut](
		ops.OpMetadata{Type: "echo"},
		func(_ ops.OpDependencies, _ context.Context, in testOpIn) (testOpOut, error) {
			return testOpOut{Output: in.Message}, nil
		},
	)
	ops.Register(testOp)
}

func TestNode_Unmarshal_Positive_Types(t *testing.T) {
	registerTestOp()
	// YAML with sequence key creates sequential workflow node [pkg/recipe/node.go]
	var n1 Node
	require.NoError(t, yamlUnmarshalStrict(`sequence: []`, &n1))
	assert.IsType(t, &NodeSequence{}, n1.NodeImpl)

	// YAML with state key creates state machine node [pkg/recipe/node.go]
	var n2 Node
	require.NoError(t, yamlUnmarshalStrict(`state: { initial: "s", states: { s: { op: echo, inputs: {message: hi}}}}`, &n2))
	assert.IsType(t, &NodeState{}, n2.NodeImpl)

	// YAML with op key creates operation execution node [pkg/recipe/node.go]
	var n3 Node
	require.NoError(t, yamlUnmarshalStrict(`op: echo
inputs: {message: hi}`, &n3))
	assert.IsType(t, &NodeOp{}, n3.NodeImpl)

	// YAML with shared key creates reusable component reference [pkg/recipe/node.go]
	var n4 Node
	require.NoError(t, yamlUnmarshalStrict(`shared: ref`, &n4))
	assert.IsType(t, &NodeShared{}, n4.NodeImpl)
}

func TestNode_Unmarshal_Invalids(t *testing.T) {
	registerTestOp()
	// Invalid YAML structure fails with specific message [pkg/recipe/node.go]
	var n Node
	err := yamlUnmarshalStrict(`{}`, &n)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "intermediate node must either be a op, sequence, state, or shared reference")

	// Unknown op errors clearly with location [pkg/recipe/node.go]
	var n2 Node
	err = yamlUnmarshalStrict("op: missing\ninputs: {}", &n2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown op")
}

func TestParser_Load_From_String_And_Reader(t *testing.T) {
	registerTestOp()
	// Valid YAML recipes load successfully from readers [pkg/recipe/parser.go]
	y := `version: 1.0
sequence:
  - op: echo
    inputs: {message: hi}
`
	r1, err := LoadRecipeFromString([]byte(y))
	require.NoError(t, err)
	assert.IsType(t, &RecipeSequence{}, r1.RecipeImpl)

	r, err := LoadRecipeFromReader(bytes.NewBufferString(y))
	require.NoError(t, err)
	assert.IsType(t, &RecipeSequence{}, r.RecipeImpl)

	// Invalid YAML syntax fails with parse error details [pkg/recipe/parser.go]
	_, err = LoadRecipeFromString([]byte("::bad"))
	require.Error(t, err)

	// Nil or empty inputs handle gracefully [pkg/recipe/parser.go]
	_, err = LoadRecipeFromReader(strings.NewReader(""))
	require.Error(t, err)
}

func TestParser_Load_PreservesConstMetadata(t *testing.T) {
	registerTestOp()

	y := `version: 1.0
sequence:
  - id: checks
    const: true
    sequence:
      - id: lint
        op: echo
        inputs: {message: hi}
`

	r, err := LoadRecipeFromString([]byte(y))
	require.NoError(t, err)

	outer := r.RecipeImpl.(*RecipeSequence)
	require.Len(t, outer.Sequence, 1)
	checks := outer.Sequence[0].NodeImpl.(*NodeSequence)
	assert.True(t, checks.Const)
	require.Len(t, checks.Sequence, 1)
	assert.False(t, checks.Sequence[0].GetMetadata().Const, "children should retain declared metadata; inheritance is runtime-only")
}

// helper for strict YAML unmarshal through recipe's yaml v3
func yamlUnmarshalStrict(s string, v interface{}) error {
	return yamlv3.Unmarshal([]byte(s), v)
}
