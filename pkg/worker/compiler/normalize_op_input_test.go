package compiler

import (
	"reflect"
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/stretchr/testify/require"
)

func TestNormalizeOpInput_ConvertsStoredRefForArtifactKeyField(t *testing.T) {
	type input struct {
		Artifact swf.ArtifactKey `json:"artifact"`
	}

	key := swf.ArtifactKey{
		JobId:       "job-1",
		TaskOrdinal: 2,
		Name:        "foo.txt",
		SizeBytes:   7,
	}

	normalized, err := NormalizeOpInput(reflectTypeOf[input](), map[string]interface{}{
		"artifact": recipeartifacts.NewStoredRef(key),
	})
	require.NoError(t, err)
	require.Equal(t, key, normalized.Data["artifact"])
	require.Equal(t, []swf.ArtifactKey{key}, normalized.StoredArtifactKeys)
}

func TestNormalizeOpInput_RejectsExternalRefForArtifactKeyField(t *testing.T) {
	type input struct {
		Artifact swf.ArtifactKey `json:"artifact"`
	}

	_, err := NormalizeOpInput(reflectTypeOf[input](), map[string]interface{}{
		"artifact": recipeartifacts.NewExternalRef("foo.txt", "https://example.com/foo.txt", false),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "artifact expects a stored artifact")
}

func TestNormalizeOpInput_CollectsStoredKeysFromTypedAndDynamicFields(t *testing.T) {
	type nested struct {
		Items []recipeartifacts.Ref `json:"items"`
	}
	type input struct {
		Artifact recipeartifacts.Ref    `json:"artifact"`
		Nested   nested                 `json:"nested"`
		Extra    map[string]interface{} `json:"-" mapstructure:",remain"`
	}

	keyA := swf.ArtifactKey{
		JobId:       "job-a",
		TaskOrdinal: 1,
		Name:        "a.txt",
		SizeBytes:   10,
	}
	keyB := swf.ArtifactKey{
		JobId:       "job-b",
		TaskOrdinal: 2,
		Name:        "b.txt",
		SizeBytes:   20,
	}
	keyC := swf.ArtifactKey{
		JobId:       "job-c",
		TaskOrdinal: 3,
		Name:        "c.txt",
		SizeBytes:   30,
	}

	normalized, err := NormalizeOpInput(reflectTypeOf[input](), map[string]interface{}{
		"artifact": recipeartifacts.NewStoredRef(keyA),
		"nested": map[string]interface{}{
			"items": []interface{}{
				recipeartifacts.NewStoredRef(keyB),
			},
		},
		"dynamic": map[string]interface{}{
			"artifact": recipeartifacts.NewStoredRef(keyC),
		},
	})
	require.NoError(t, err)
	require.Equal(t, recipeartifacts.NewStoredRef(keyA), normalized.Data["artifact"])
	require.ElementsMatch(t, []swf.ArtifactKey{keyA, keyB, keyC}, normalized.StoredArtifactKeys)
}

func TestNormalizeOpInput_PreservesUnknownFieldsForValidation(t *testing.T) {
	type input struct {
		Message string `json:"message"`
	}

	normalized, err := NormalizeOpInput(reflectTypeOf[input](), map[string]interface{}{
		"message": "hello",
		"extra":   "value",
	})
	require.NoError(t, err)
	require.Equal(t, "value", normalized.Data["extra"])
}

func reflectTypeOf[T any]() reflect.Type {
	var zero T
	return reflect.TypeOf(zero)
}
