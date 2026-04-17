package recipe

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvalidOp(t *testing.T) {
	_, err := LoadRecipeFromString([]byte(r2))
	require.EqualErrorf(t, err, "unknown op: [my_invalid_op] at [2:1]", "invalid op")
}

const r2 = `
id: invalid_op_test
version: 1.0.0
op: my_invalid_op
`

type EchoIn struct {
	Message string `yaml:"message"`
}

type EchoOut struct {
	Output string `yaml:"output"`
}

func registerOp() {
	echoActivity := ops.NewActivityMappedOpV2[EchoIn, EchoOut](
		ops.OpMetadata{Type: "echo"},
		func(_ ops.OpDependencies, _ context.Context, input EchoIn) (EchoOut, error) {
			message := input.Message
			return EchoOut{
				Output: message,
			}, nil
		},
	)
	ops.Register(echoActivity)
}

func TestSharedResolution(t *testing.T) {
	registerOp()
	r, err := LoadRecipeFromString([]byte(shared))
	require.NoError(t, err)
	seq := r.RecipeImpl.(*RecipeSequence)
	item := seq.Sequence[0]
	//assert that the shared node was resolved
	assert.NotNil(t, item.NodeImpl)
	assert.IsType(t, &NodeOp{}, item.NodeImpl)
}

const shared = `
id: invalid_op_test
version: 1.0.0
sequence: 
  - shared: foo
defs: 
  foo:
    op: echo
    inputs:
      message: hi
`
