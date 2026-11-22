package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type unreadableInvocationArtifact struct {
	jobdb.Artifact
	err error
}

func (a unreadableInvocationArtifact) Bytes(context.Context) ([]byte, error) { return nil, a.err }
func TestInvocationInfrastructureArtifactFailures(t *testing.T) {
	original := errors.New("artifact storage failed")
	for _, tc := range []struct {
		name      string
		artifacts []jobdb.Artifact
		reason    jobdb.ExecutionInfrastructureReason
	}{
		{"missing", nil, jobdb.ExecutionInfrastructureDurableDecode},
		{"decode", []jobdb.Artifact{jobdb.NewArtifactFromBytes("private-input", []byte("{"))}, jobdb.ExecutionInfrastructureDurableDecode},
		{"read", []jobdb.Artifact{unreadableInvocationArtifact{jobdb.NewArtifactFromBytes("private-input", nil), original}}, jobdb.ExecutionInfrastructureRuntimeIO},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := ActivityInvocationRequest{InputArtifact: "private-input"}
			_, err := in.RestoreInput(context.Background(), tc.artifacts)
			var infra *jobdb.ExecutionInfrastructureError
			require.ErrorAs(t, err, &infra)
			require.Equal(t, tc.reason, infra.Reason)
			if tc.name == "read" {
				require.ErrorIs(t, err, original)
			}
		})
	}
}
