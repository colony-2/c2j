package objects

import (
	"context"
	"errors"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestCheckpointInfrastructurePreservesCauseAndSource(t *testing.T) {
	store, artifacts := testStore(t)
	ctx := context.Background()
	ref, err := store.Publish(ctx, "test.session/v1", map[string]any{"id": "stable"}, nil)
	require.NoError(t, err)
	original := errors.New("artifact backend unavailable")
	store.config.GetArtifact = func(jobdb.ArtifactKey) (jobdb.Artifact, error) { return nil, original }
	_, err = store.Open(ctx, ref, ref.Type)
	require.ErrorIs(t, err, original)
	var infra *jobdb.ExecutionInfrastructureError
	require.ErrorAs(t, err, &infra)
	require.Equal(t, jobdb.ExecutionInfrastructureRuntimeIO, infra.Reason)
	require.Equal(t, &jobdb.JobKey{TenantId: ref.TenantID, JobId: ref.Artifact.JobId}, infra.JobKey)
	store.config.GetArtifact = func(key jobdb.ArtifactKey) (jobdb.Artifact, error) { return (*artifacts)[key.Name], nil }
	(*artifacts)[ref.Artifact.Name] = jobdb.NewArtifactFromBytes(ref.Artifact.Name, []byte("corrupt checkpoint"))
	_, err = store.Open(ctx, ref, ref.Type)
	require.ErrorAs(t, err, &infra)
	require.Equal(t, jobdb.ExecutionInfrastructureDurableDecode, infra.Reason)
	bad := ref
	bad.TenantID = "other-tenant"
	_, err = store.Open(ctx, bad, ref.Type)
	require.Error(t, err)
	require.False(t, jobdb.IsExecutionInfrastructureError(err), "invalid caller references remain validation failures")
}
