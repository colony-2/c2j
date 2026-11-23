package gitshallow

import (
	"context"

	"github.com/colony-2/c2j/pkg/git/internal/transportclone"
)

// GitShallowCloneInput represents the input parameters for the GitShallowClone activity
type GitShallowCloneInput struct {
	SourceDir  string `json:"sourceDir"`
	TargetDir  string `json:"targetDir"`
	CommitHash string `json:"commitHash"`
}

// GitShallowCloneOutput represents the output of the GitShallowClone activity
type GitShallowCloneOutput struct {
	ClonedPath string `json:"clonedPath"`
}

// GitShallowClone performs a shallow clone of a local git repository to another directory
// This is a  activity that can be invoked as part of recipe workflows
func GitShallowClone(ctx context.Context, input GitShallowCloneInput) (*GitShallowCloneOutput, error) {
	if err := transportclone.Clone(ctx, input.SourceDir, input.TargetDir, input.CommitHash, transportclone.DepthOne); err != nil {
		return nil, err
	}

	return &GitShallowCloneOutput{
		ClonedPath: input.TargetDir,
	}, nil
}
