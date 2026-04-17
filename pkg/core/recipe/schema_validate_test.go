package recipe

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/core/ops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type schemaEchoIn struct {
	Msg string `json:"msg" yaml:"msg"`
}
type schemaEchoOut struct {
	Out string `json:"out" yaml:"out"`
}

func TestSchema_Generate_Includes_Ops_And_NodeTypes(t *testing.T) {
	ops.Clear()
	// Recipe schemas include all registered operations dynamically [pkg/recipe/schema.go]
	op := ops.NewActivityMappedOpV2[schemaEchoIn, schemaEchoOut](
		ops.OpMetadata{Type: "echo"},
		func(_ ops.OpDependencies, _ context.Context, in schemaEchoIn) (schemaEchoOut, error) {
			return schemaEchoOut{Out: in.Msg}, nil
		},
	)
	ops.Register(op)

	s, err := GenerateSchemaString()
	require.NoError(t, err)
	assert.Contains(t, s, "\"Node\"")
	assert.Contains(t, s, "Sequence")
	assert.Contains(t, s, "State")
	assert.Contains(t, s, "echo")

	// Empty operation registry handles gracefully [pkg/recipe/schema.go]
	ops.Clear()
	s2, err := GenerateSchemaString()
	require.NoError(t, err)
	assert.Contains(t, s2, "Sequence")
	assert.Contains(t, s2, "State")
}

func TestValidate_Valid_And_Invalid_YAML(t *testing.T) {
	// Use a root-op recipe to avoid Node oneOf ambiguities
	registerTestOp()
	validOp := "version: \"1.0\"\nop: echo\ninputs: {message: hi}\n"
	// Valid YAML recipes pass schema validation [pkg/recipe/validate.go]
	require.NoError(t, Validate(validOp))

	// Invalid YAML syntax fails with parse errors [pkg/recipe/validate.go]
	assert.Error(t, Validate("::bad"))

	// Missing required fields: currently inputs are optional at schema level
	// Concrete ops may enforce required fields at runtime
}
