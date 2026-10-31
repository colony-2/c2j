package compiler

import (
	"reflect"
	"testing"

	"github.com/colony-2/c2j/pkg/input"
	"github.com/stretchr/testify/require"
)

func TestReviewPresentationOutputPreservesAbsence(t *testing.T) {
	typ := reflect.TypeFor[input.InputForm]()
	legacy := map[string]any{"question": "legacy"}
	normalized := normalizeOpOutput(typ, legacy)
	require.NotContains(t, normalized, "document_metadata")
	require.NotContains(t, normalized, "overview_document_id")
	// Other pre-existing zero filling remains unchanged.
	require.Contains(t, normalized, "kind")
	metadata := map[string]any{"brief": map[string]any{"order": 0}}
	supplied := normalizeOpOutput(typ, map[string]any{"document_metadata": metadata, "overview_document_id": "brief"})
	require.Equal(t, metadata, supplied["document_metadata"])
	require.Equal(t, "brief", supplied["overview_document_id"])
	placeholders, err := zeroOutputFromType(typ)
	require.NoError(t, err)
	require.Contains(t, placeholders, "document_metadata")
	require.Contains(t, placeholders, "overview_document_id")
}
