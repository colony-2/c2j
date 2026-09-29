package compiler

import (
	"fmt"
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestArtifactDependenciesHaveStableOrder(t *testing.T) {
	keys := make([]jobdb.ArtifactKey, 16)
	for i := range keys {
		keys[i] = jobdb.ArtifactKey{JobId: "source", TaskOrdinal: 1, Name: fmt.Sprintf("artifact-%02d", i), SizeBytes: 1}
	}
	for shift := range len(keys) {
		bindings := map[string]recipeartifacts.Ref{}
		for i := range keys {
			j := (i + shift) % len(keys)
			bindings[keys[j].Name] = recipeartifacts.NewStoredRef(keys[j])
		}
		// Typed inputs and explicit bindings can refer to the same dependency.
		require.Equal(t, keys, appendArtifactKeys([]jobdb.ArtifactKey{keys[5], keys[0]}, bindings))
	}
}
