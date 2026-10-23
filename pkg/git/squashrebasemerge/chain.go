package squashrebasemerge

import (
	"context"
	"fmt"
	"strings"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	"github.com/colony-2/c2j/pkg/ops"
)

type publicationState struct {
	Input      SquashRebaseMergeInput `json:"input"`
	Range      SquashRangeSummary     `json:"range"`
	TargetHash string                 `json:"target_hash"`
	RemoteRef  string                 `json:"remote_ref"`
	Candidate  string                 `json:"candidate"`
	EmptyBase  bool                   `json:"empty_base"`
	NoChanges  bool                   `json:"no_changes"`
}

func checkpoint(inv ops.OpDependencies, state publicationState) publicationState {
	// A later task can run on another machine. Never persist a managed path.
	if inv != nil && state.Input.RepoPath == inv.WorktreePath() {
		state.Input.RepoPath = contextual.WorktreePathSentinel
	}
	return state
}

func preparePublication(inv ops.OpDependencies, ctx context.Context, input SquashRebaseMergeInput) (publicationState, error) {
	return preparePublicationFrom(ctx, inv, input, managedEmptyBase(inv, input.RepoPath))
}

func preparePublicationFrom(ctx context.Context, inv ops.OpDependencies, input SquashRebaseMergeInput, emptyBase bool) (publicationState, error) {
	var zero publicationState
	if err := requireCleanWorkspace(ctx, input.RepoPath); err != nil {
		return zero, err
	}
	if input.UpstreamRepo == "" {
		return zero, fmt.Errorf("upstream_repo is required")
	}
	if strings.TrimSpace(input.UpstreamBranch) == "" {
		input.UpstreamBranch = defaultTargetBranch
	}
	branch := "refs/heads/" + strings.TrimPrefix(input.UpstreamBranch, "refs/heads/")
	if _, err := common.ExecuteGitCommand(ctx, input.RepoPath, "check-ref-format", branch); err != nil {
		return zero, err
	}
	head := input.LocalHash
	if head == "" {
		head = "HEAD"
	}
	head, err := common.GetCommitHash(ctx, input.RepoPath, head+"^{commit}")
	if err != nil {
		return zero, err
	}
	state := publicationState{Input: input, EmptyBase: emptyBase, RemoteRef: branch}
	state.Range.PersistHash = head
	if !emptyBase {
		// Resolve the destination once, then carry its exact SHA across tasks.
		// Fetch the requested repository, not a possibly unrelated sole remote.
		if err := fetchTargetBranch(ctx, input.RepoPath, input.UpstreamRepo, branch); err != nil {
			return zero, err
		}
		state.TargetHash, err = common.GetCommitHash(ctx, input.RepoPath, "FETCH_HEAD")
		if err != nil {
			return zero, err
		}
		state.Range.BaseHash, err = mergeBase(ctx, input.RepoPath, head, state.TargetHash)
		if err != nil {
			// A fresh task clone may have the destination tip at a shallow
			// boundary. Fetch enough of its history to discover the merge base.
			anchor := head
			if inv != nil && input.RepoPath == inv.WorktreePath() && inv.GitContext().ResolvedBaseHash != "" {
				anchor = inv.GitContext().ResolvedBaseHash
			}
			if _, deepenErr := hasAncestor(ctx, input.RepoPath, input.UpstreamRepo, anchor, state.TargetHash); deepenErr != nil {
				return zero, deepenErr
			}
			state.Range.BaseHash, err = mergeBase(ctx, input.RepoPath, head, state.TargetHash)
			if err != nil {
				return zero, err
			}
		}
		if remote, err := determineRemoteName(ctx, workspaceSnapshot{RepoPath: input.RepoPath, UpstreamRepo: input.UpstreamRepo}); err == nil {
			state.RemoteRef = remoteTrackingRef(remote, input.UpstreamBranch)
		}
		if input.Rebase != nil && !*input.Rebase {
			ok, err := isAncestor(ctx, input.RepoPath, state.TargetHash, head)
			if err != nil {
				return zero, err
			}
			if !ok {
				return zero, common.NotFastForwardError{TargetBranch: input.UpstreamBranch, LocalHead: head, UpstreamHead: state.TargetHash}
			}
		}
	}
	if _, err := common.ExecuteGitCommand(ctx, input.RepoPath, "reset", "--hard", head); err != nil {
		return zero, err
	}
	squashed, err := squash(ctx, SquashInput{RepoPath: input.RepoPath, BaseHash: state.Range.BaseHash, Author: input.Author, CommitMessage: input.CommitMessage}, emptyBase)
	if err != nil {
		return zero, err
	}
	state.Candidate = squashed.SquashedHash
	state.NoChanges = !emptyBase && head == state.Range.BaseHash
	return checkpoint(inv, state), nil
}

func rebasePublication(inv ops.OpDependencies, ctx context.Context, input publicationState) (publicationState, error) {
	state := input
	if err := verifyCandidate(ctx, state); err != nil {
		return publicationState{}, err
	}
	if !state.EmptyBase && (state.Input.Rebase == nil || *state.Input.Rebase) {
		out, err := thinpackrebase.Run(ctx, thinpackrebase.ThinpackRebaseInput{
			RepoPath: state.Input.RepoPath, BaseHash: state.Range.BaseHash,
			PersistHash: state.Candidate, TargetBaseHash: state.TargetHash,
			BaseRepo: state.Input.UpstreamRepo, UpstreamRemote: state.Input.UpstreamRepo,
		})
		if err != nil {
			return publicationState{}, err
		}
		state.Candidate = out.NewPersistHash
		if err := setPublishedBase(inv, mergeInput(state), out.NewBaseHash); err != nil {
			return publicationState{}, err
		}
	}
	return checkpoint(inv, state), nil
}

func verifyCandidate(ctx context.Context, state publicationState) error {
	if err := requireCleanWorkspace(ctx, state.Input.RepoPath); err != nil {
		return err
	}
	head, err := common.GetCommitHash(ctx, state.Input.RepoPath, "HEAD")
	if err != nil {
		return err
	}
	if state.Candidate == "" || head != state.Candidate {
		return fmt.Errorf("workspace HEAD %s does not match saved publication candidate %s", head, state.Candidate)
	}
	return nil
}

func mergeInput(state publicationState) MergeInput {
	return MergeInput{RepoPath: state.Input.RepoPath, UpstreamRepo: state.Input.UpstreamRepo, UpstreamBranch: state.Input.UpstreamBranch}
}

func mergePublication(inv ops.OpDependencies, ctx context.Context, input publicationState) (SquashRebaseMergeOutput, error) {
	var zero SquashRebaseMergeOutput
	state := input
	if err := verifyCandidate(ctx, state); err != nil {
		return zero, err
	}
	if !state.NoChanges {
		source := state.Input.UpstreamRepo
		if inv != nil && inv.WorktreePath() == state.Input.RepoPath {
			source = inv.GitContext().BaseRepo
		}
		if _, err := mergeFrom(ctx, mergeInput(state), state.EmptyBase, source); err != nil {
			return zero, err
		}
	}
	// Preserve the prepared snapshot as child/result evidence, as the original
	// combined op did. An empty workspace must leave empty-base mode on publish.
	if state.EmptyBase {
		if err := setPublishedBase(inv, mergeInput(state), state.Candidate); err != nil {
			return zero, err
		}
	}
	branch := state.Input.UpstreamBranch
	if state.EmptyBase {
		branch = "refs/heads/" + strings.TrimPrefix(branch, "refs/heads/")
	}
	return SquashRebaseMergeOutput{
		TargetBranch: branch, RemoteRef: state.RemoteRef, MergedHash: state.Candidate,
		SquashedCommits: state.Range,
		FastForward:     !state.EmptyBase && state.Input.Rebase != nil && !*state.Input.Rebase,
		GitContextPatch: map[string]interface{}{
			"base_hash": state.Candidate, "persist_hash": state.Candidate, "previous_hash": state.Range.PersistHash,
		},
	}, nil
}
