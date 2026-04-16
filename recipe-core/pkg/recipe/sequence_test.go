package recipe

import (
    "testing"

    yamlv3 "gopkg.in/yaml.v3"
    "github.com/stretchr/testify/require"
)

func TestSequence_YAML_And_Order(t *testing.T) {
    registerTestOp()
    // Sequences execute nodes in defined order [pkg/recipe/sequence.go]
    seq := &RecipeSequence{SequenceData: SequenceData{Sequence: []Node{{NodeImpl: &NodeOp{NodeMetadata: NodeMetadata{ID: "a"}, OpData: OpData{Op: "echo"}}}, {NodeImpl: &NodeOp{NodeMetadata: NodeMetadata{ID: "b"}, OpData: OpData{Op: "echo"}}}}}}
    b, err := yamlv3.Marshal(seq)
    require.NoError(t, err)
    // Round-trip should match Unmarshal shape
    var back RecipeSequence
    require.NoError(t, yamlv3.Unmarshal(b, &back))
    require.Len(t, back.Sequence, 2)

    // Empty sequences handle without errors [pkg/recipe/sequence.go]
    var empty RecipeSequence
    b2, err := yamlv3.Marshal(&empty)
    require.NoError(t, err)
    var back2 RecipeSequence
    require.NoError(t, yamlv3.Unmarshal(b2, &back2))
}
