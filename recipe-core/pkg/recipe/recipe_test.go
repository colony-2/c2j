package recipe

import (
    "testing"

    yamlv3 "gopkg.in/yaml.v3"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestRecipe_Metadata_And_YAML(t *testing.T) {
    registerTestOp()
    s := &Recipe{RecipeImpl: &RecipeSequence{RecipeMetadata: RecipeMetadata{Version: "1.0", NodeMetadata: NodeMetadata{ID: "sid"}}}}
    st := &Recipe{RecipeImpl: &RecipeState{RecipeMetadata: RecipeMetadata{Version: "1.0", NodeMetadata: NodeMetadata{ID: "stid"}}}}
    o := &Recipe{RecipeImpl: &RecipeSequence{RecipeMetadata: RecipeMetadata{Version: "1.0", NodeMetadata: NodeMetadata{ID: "oid"}}, SequenceData: SequenceData{Sequence: []Node{{NodeImpl: &NodeOp{OpData: OpData{Op: "echo"}}}}}}}

    // State machine, sequential and single op recipes retrieve metadata correctly [pkg/recipe/recipe.go]
    assert.Equal(t, "sid", s.GetMetdata().ID)
    assert.Equal(t, "stid", st.GetMetdata().ID)
    assert.Equal(t, "oid", o.GetMetdata().ID)

    // Recipes serialize to YAML preserving all fields [pkg/recipe/recipe.go]
    b, err := yamlv3.Marshal(o)
    require.NoError(t, err)
    var r2 Recipe
    require.NoError(t, yamlv3.Unmarshal(b, &r2))
    assert.IsType(t, &RecipeSequence{}, r2.RecipeImpl)

    // Missing recipe type in YAML fails with error [pkg/recipe/recipe.go]
    var bad Recipe
    err = yamlv3.Unmarshal([]byte("version: 1.0\nid: x"), &bad)
    require.Error(t, err)
}
