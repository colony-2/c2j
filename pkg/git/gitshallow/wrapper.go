package gitshallow

import (
	"context"

	"github.com/colony-2/c2j/pkg/core/ops"
)

// GitShallowInput defines the input for git shallow clone activities - ALL fields MUST have json tags
type GitShallowInput struct {
	SourceDir  string `json:"source_dir" default:"{{ context.environment.worktree_path }}" validate:"required,dir"` // Required: path to source git repository
	TargetDir  string `json:"target_dir" validate:"required"`                                                       // Required: path where to clone
	CommitHash string `json:"commit_hash" validate:"required,hexadecimal,min=7,max=40"`                             // Required: commit hash to checkout
}

// GitShallowOutput defines the output from git shallow clone activities - ALL fields MUST have json tags
type GitShallowOutput struct {
	ClonedPath string `json:"cloned_path"` // Path to the cloned repository
}

// GitShallowActivityWrapper implements the RegisterableOp interface
type GitShallowActivityWrapper struct{}

// NewGitShallowActivity creates a new git shallow clone activity that implements RegisterableOp
func NewGitShallowActivity() *GitShallowActivityWrapper {
	return &GitShallowActivityWrapper{}
}

func GetOp() ops.RegisterableOp {
	a := NewGitShallowActivity()
	return ops.NewActivityMappedOpV2[GitShallowInput, GitShallowOutput](a.GetMetadata(), a.Execute)
}

// GetMetadata returns activity metadata for registration
func (a *GitShallowActivityWrapper) GetMetadata() ops.OpMetadata {
	return ops.OpMetadata{
		Type:        "git_shallow_clone",
		Description: "Performs a shallow clone of a local git repository to another directory at a specific commit",
		Version:     "1.0.0",
	}
}

// Execute runs the activity with provided configuration and inputs
func (a *GitShallowActivityWrapper) Execute(_ ops.OpDependencies, ctx context.Context, input GitShallowInput) (GitShallowOutput, error) {
	// Build the clone input
	cloneInput := GitShallowCloneInput{
		SourceDir:  input.SourceDir,
		TargetDir:  input.TargetDir,
		CommitHash: input.CommitHash,
	}

	// Execute the clone
	output, err := GitShallowClone(ctx, cloneInput)
	if err != nil {
		return GitShallowOutput{}, err
	}

	return GitShallowOutput{
		ClonedPath: output.ClonedPath,
	}, nil
}
