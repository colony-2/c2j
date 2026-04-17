package squashrebasemerge

import (
	"context"

	"github.com/colony-2/c2j/pkg/core/ops"
)

// SquashRebaseMergeInput defines the parameters for the squash-rebase-merge git op.
type SquashRebaseMergeInput struct {
	RepoPath       string `json:"repo_path,omitempty" default:"{{ context.environment.worktree_path }}" validate:"required,dir"`
	LocalHash      string `json:"local_hash,omitempty" validate:"omitempty,hexadecimal,min=7,max=40"`
	UpstreamRepo   string `json:"upstream_repo" default:"{{ context.git.repo }}" validate:"required"`
	UpstreamBranch string `json:"upstream_branch,omitempty" default:"{{ context.git.ref }}" validate:"required"`
	Rebase         *bool  `json:"rebase,omitempty"`
	Author         string `json:"author,omitempty" default:"{{ context.git.author }}"`
	CommitMessage  string `json:"commit_message,omitempty"`
}

// SquashRangeSummary captures the original commit range that was squashed.
type SquashRangeSummary struct {
	BaseHash    string `json:"base_hash"`
	PersistHash string `json:"persist_hash"`
}

// SquashRebaseMergeOutput returns metadata about the merge operation.
type SquashRebaseMergeOutput struct {
	TargetBranch    string                 `json:"target_branch"`
	RemoteRef       string                 `json:"remote_ref"`
	MergedHash      string                 `json:"merged_hash"`
	SquashedCommits SquashRangeSummary     `json:"squashed_commits"`
	GitContextPatch map[string]interface{} `json:"git_context_patch,omitempty"`
	FastForward     bool                   `json:"fast_forward"`
}

// Activity implements the RegisterableOp interface for squash-rebase-merge.
type Activity struct{}

// NewActivity constructs the squash-rebase-merge activity wrapper.
func NewActivity() *Activity {
	return &Activity{}
}

// GetOp exposes the operation to recipe registration.
func GetOp() ops.RegisterableOp {
	return ops.NewActivityMappedOpV2[SquashRebaseMergeInput, SquashRebaseMergeOutput](NewActivity().GetMetadata(), func(inv ops.OpDependencies, ctx context.Context, input SquashRebaseMergeInput) (SquashRebaseMergeOutput, error) {
		result, err := Run(ctx, input)
		if err != nil {
			return SquashRebaseMergeOutput{}, err
		}
		return *result, nil
	})
}

// GetMetadata describes the operation for discovery and documentation.
func (a *Activity) GetMetadata() ops.OpMetadata {
	return ops.OpMetadata{
		Type:        "squashrebasemerge",
		Description: "Squashes local commits, rebases onto the latest target branch tip, and fast-forward merges back to the remote",
		Version:     "1.0.0",
	}
}
