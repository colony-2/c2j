package artifacts

import (
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type namedStoredArtifact struct {
	jobdb.Artifact
	key jobdb.ArtifactKey
}

func (a namedStoredArtifact) ArtifactKey() (jobdb.ArtifactKey, error) { return a.key, nil }

func TestRefFromArtifactPreservesLogicalNameAndStorageKey(t *testing.T) {
	key := jobdb.ArtifactKey{JobId: "job", TaskOrdinal: 7, Name: "storage-name", SizeBytes: 4}
	ref, err := RefFromArtifact(namedStoredArtifact{jobdb.NewArtifactFromBytes("result.txt", []byte("data")), key})
	require.NoError(t, err)
	require.Equal(t, "result.txt", ref.NameValue())
	stored, ok := ref.StoredKey()
	require.True(t, ok)
	require.Equal(t, key, stored)
	require.NoError(t, ref.Validate())
}
