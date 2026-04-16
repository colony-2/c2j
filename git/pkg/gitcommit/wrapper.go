package gitcommit

import (
	"context"
	"time"

	"github.com/colony-2/c2j/recipe-core/pkg/ops"
)

// PersistCommitInput defines the input for persist commit activities - ALL fields MUST have json tags
type PersistCommitInput struct {
	RepoPath        string        `json:"repo_path" default:"{{ context.environment.worktree_path }}" validate:"required,dir"`              // Required: path to the local Git repository
	StorageLocation string        `json:"storage_location" validate:"required"`                                                             // Required: directory path where thin packs will be stored
	RootHash        string        `json:"root_hash" default:"{{ context.git.resolved_hash }}" validate:"required,hexadecimal,min=7,max=40"` // Required: base commit hash this set was built upon
	CommitMessage   string        `json:"commit_message,omitempty"`                                                                         // Optional: message for the commit
	Author          string        `json:"author,omitempty" default:"{{ context.git.author }}"`                                              // Optional: author name and email
	Timeout         time.Duration `json:"timeout,omitempty"`                                                                                // Optional: operation timeout
}

// PersistCommitActivityWrapper implements the RegisterableOp interface
type PersistCommitActivityWrapper struct{}

func GetPersistOp() ops.RegisterableOp {
	p := &PersistCommitActivityWrapper{}
	return ops.NewActivityMappedOpV2[PersistCommitInput, PersistCommitOutput](p.GetMetadata(), p.Execute)
}

// GetMetadata returns activity metadata for registration
func (a *PersistCommitActivityWrapper) GetMetadata() ops.OpMetadata {
	return ops.OpMetadata{
		Type:        "git_persist_commit",
		Description: "Capture a Git commit and generate a portable thin pack for external storage",
		Version:     "1.0.0",
	}
}

// Execute runs the activity with provided configuration and inputs
func (a *PersistCommitActivityWrapper) Execute(_ ops.OpDependencies, ctx context.Context, input PersistCommitInput) (PersistCommitOutput, error) {
	// Build the persist input
	persistInput := PersistCommitActivity{
		RepoPath:        input.RepoPath,
		StorageLocation: input.StorageLocation,
		RootHash:        input.RootHash,
		CommitMessage:   input.CommitMessage,
		Author:          input.Author,
		Timeout:         input.Timeout,
	}

	// Execute the persist operation
	output, err := PersistCommit(ctx, persistInput)
	if err != nil {
		return PersistCommitOutput{}, err
	}

	return *output, nil
}

// RestoreCommitInput defines the input for restore commit activities - ALL fields MUST have json tags
type RestoreCommitInput struct {
	RepoPath        string        `json:"repo_path" default:"{{ context.environment.worktree_path }}" validate:"required,dir"`              // Required: path to the local Git repository
	TargetCommit    string        `json:"target_commit" validate:"required,hexadecimal,min=7,max=40"`                                       // Required: commit hash to restore to
	RootHash        string        `json:"root_hash" default:"{{ context.git.resolved_hash }}" validate:"required,hexadecimal,min=7,max=40"` // Required: root commit hash for this set
	StorageLocation string        `json:"storage_location" validate:"required"`                                                             // Required: directory containing thin packs
	Force           bool          `json:"force,omitempty"`                                                                                  // Optional: force checkout even with uncommitted changes
	Timeout         time.Duration `json:"timeout,omitempty"`                                                                                // Optional: operation timeout
}

// RestoreCommitActivityWrapper implements the RegisterableOp interface
type RestoreCommitActivityWrapper struct{}

// NewRestoreCommitActivity creates a new restore commit activity that implements RegisterableOp
func NewRestoreCommitActivity() *RestoreCommitActivityWrapper {
	return &RestoreCommitActivityWrapper{}
}

func GetRestoreOp() ops.RegisterableOp {
	r := &RestoreCommitActivityWrapper{}
	return ops.NewActivityMappedOpV2[RestoreCommitInput, RestoreCommitOutput](r.GetMetadata(), r.Execute)
}

// GetMetadata returns activity metadata for registration
func (a *RestoreCommitActivityWrapper) GetMetadata() ops.OpMetadata {
	return ops.OpMetadata{
		Type:        "git_restore_commit",
		Description: "Restore a specific commit state, rebuilding from thin packs if necessary",
		Version:     "1.0.0",
	}
}

// Execute runs the activity with provided configuration and inputs
func (a *RestoreCommitActivityWrapper) Execute(_ ops.OpDependencies, ctx context.Context, input RestoreCommitInput) (RestoreCommitOutput, error) {
	// Build the restore input
	restoreInput := RestoreCommitActivity{
		RepoPath:        input.RepoPath,
		TargetCommit:    input.TargetCommit,
		RootHash:        input.RootHash,
		StorageLocation: input.StorageLocation,
		Force:           input.Force,
		Timeout:         input.Timeout,
	}

	// Execute the restore operation
	output, err := RestoreCommit(ctx, restoreInput)
	if err != nil {
		return RestoreCommitOutput{}, err
	}

	return *output, nil
}
