package recipe

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHash_Deterministic_And_Changes(t *testing.T) {
	h := NewHashComputer()

	r1 := &Recipe{RecipeImpl: &RecipeSequence{RecipeMetadata: RecipeMetadata{Version: "1.0", NodeMetadata: NodeMetadata{ID: "r"}}, SequenceData: SequenceData{}}}
	r2 := &Recipe{RecipeImpl: &RecipeSequence{RecipeMetadata: RecipeMetadata{Version: "1.0", NodeMetadata: NodeMetadata{ID: "r"}}, SequenceData: SequenceData{}}}
	h1 := h.ComputeRecipeHash(r1)
	h2 := h.ComputeRecipeHash(r2)

	// Identical recipes produce identical hashes for caching [pkg/recipe/hash.go]
	assert.Equal(t, h1, h2)

	// Modified recipes generate different hashes for versioning [pkg/recipe/hash.go]
	r2.RecipeImpl.(*RecipeSequence).NodeMetadata.Desc = "changed"
	h3 := h.ComputeRecipeHash(r2)
	assert.NotEqual(t, h1, h3)

	// Hash computation remains deterministic across runs [pkg/recipe/hash.go]
	assert.Equal(t, h3, h.ComputeRecipeHash(r2))
}

func TestHash_Circular_Fallback(t *testing.T) {
	// Circular references fail gracefully with fallback hashing [pkg/recipe/hash.go]
	h := NewHashComputer()
	seq := &NodeSequence{}
	cyc := Node{NodeImpl: seq}
	// create self reference cycle
	seq.Sequence = append(seq.Sequence, cyc)

	r := &Recipe{RecipeImpl: &RecipeSequence{RecipeMetadata: RecipeMetadata{Version: "v", NodeMetadata: NodeMetadata{ID: "id"}}, SequenceData: SequenceData{Sequence: []Node{{NodeImpl: seq}}}}}
	got := h.ComputeRecipeHash(r)
	assert.NotEmpty(t, got)
}
