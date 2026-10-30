package ops

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// NewInvocationTaskData keeps bulk input data in the existing artifact channel.
// Execution metadata remains in the envelope for workspace forwarding. Small
// legacy inputs keep their representation; completed inputs never rebuild.
func NewInvocationTaskData(in ActivityInvocationRequest) (jobdb.TaskData, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if len(raw) <= 128*1024 {
		return &jobdb.SimpleTaskData{Data: raw}, nil
	}
	payload, err := json.Marshal(ActivityInvocationRequest{Input: in.Input, Artifacts: in.Artifacts, Preparation: in.Preparation})
	if err != nil {
		return nil, err
	}
	in.Input, in.Artifacts, in.Preparation = nil, nil, PreparationRequest{}
	in.InputArtifact = "c2j-invocation-input.json"
	return jobdb.NewTaskData(in, jobdb.NewArtifactFromBytes(in.InputArtifact, payload))
}

// RestoreInput consumes the private attachment so it is not exposed as an op
// input file. It works with in-memory artifacts and recorded stored artifacts.
func (in *ActivityInvocationRequest) RestoreInput(ctx context.Context, artifacts []jobdb.Artifact) ([]jobdb.Artifact, error) {
	if in.InputArtifact == "" {
		return artifacts, nil
	}
	for i, artifact := range artifacts {
		if artifact.Name() != in.InputArtifact {
			continue
		}
		raw, err := artifact.Bytes(ctx)
		if err != nil {
			return nil, err
		}
		var payload ActivityInvocationRequest
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, fmt.Errorf("invalid invocation input artifact: %w", err)
		}
		in.Input, in.Artifacts, in.Preparation = payload.Input, payload.Artifacts, payload.Preparation
		in.InputArtifact = ""
		return append(artifacts[:i:i], artifacts[i+1:]...), nil
	}
	return nil, fmt.Errorf("missing invocation input artifact %q", in.InputArtifact)
}
