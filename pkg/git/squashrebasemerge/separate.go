package squashrebasemerge

import (
	"context"
	"fmt"
	"strings"

	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/ops"
)

type SquashInput struct {
	RepoPath      string `json:"repo_path,omitempty" default:"{{ context.environment.worktree_path }}" validate:"required,dir"`
	BaseHash      string `json:"base_hash,omitempty" default:"{{ context.git.execution.base_hash }}" validate:"omitempty,hexadecimal,min=7,max=40"`
	Author        string `json:"author,omitempty" default:"{{ context.git.author }}"`
	CommitMessage string `json:"commit_message,omitempty"`
}

type SquashOutput struct {
	SquashedHash    string             `json:"squashed_hash"`
	SquashedCommits SquashRangeSummary `json:"squashed_commits"`
}

type MergeInput struct {
	RepoPath       string `json:"repo_path,omitempty" default:"{{ context.environment.worktree_path }}" validate:"required,dir"`
	UpstreamRepo   string `json:"upstream_repo" default:"{{ context.git.repo }}" validate:"required"`
	UpstreamBranch string `json:"upstream_branch" default:"{{ context.git.ref }}" validate:"required"`
}

type MergeOutput struct {
	MergedHash   string `json:"merged_hash"`
	TargetBranch string `json:"target_branch"`
}

func GetSquashOp() ops.RegisterableOp {
	return ops.NewActivityMappedOpV2[SquashInput, SquashOutput](ops.OpMetadata{
		Type: "squash", Version: "1.0.0",
		Description: "Squashes local commits into one commit without rebasing or publishing",
	}, executeSquash)
}

func GetMergeOp() ops.RegisterableOp {
	return ops.NewActivityMappedOpV2[MergeInput, MergeOutput](ops.OpMetadata{
		Type: "merge", Version: "1.0.0",
		Description: "Publishes the current commit by fast-forward without squashing or rebasing",
	}, executeMerge)
}

func managedEmptyBase(inv ops.OpDependencies, path string) bool {
	if inv == nil || inv.WorktreePath() != path {
		return false
	}
	g := inv.GitContext()
	return g.Workspace.HasEmptyBase(g.ResolvedBaseHash, g.ParentHash)
}

func executeSquash(inv ops.OpDependencies, ctx context.Context, input SquashInput) (SquashOutput, error) {
	return squash(ctx, input, managedEmptyBase(inv, input.RepoPath))
}

func squash(ctx context.Context, input SquashInput, emptyBase bool) (SquashOutput, error) {
	var result SquashOutput
	if err := requireCleanWorkspace(ctx, input.RepoPath); err != nil {
		return result, err
	}
	head, err := common.GetCommitHash(ctx, input.RepoPath, "HEAD")
	if err != nil {
		return result, err
	}
	base := input.BaseHash
	if base == "" && !emptyBase {
		return result, fmt.Errorf("base_hash is required outside an empty workspace")
	}
	if base != "" {
		base, err = common.GetCommitHash(ctx, input.RepoPath, base+"^{commit}")
		if err != nil {
			return result, err
		}
		ok, err := isAncestor(ctx, input.RepoPath, base, head)
		if err != nil {
			return result, err
		}
		if !ok {
			return result, fmt.Errorf("squash base must be an ancestor of HEAD")
		}
	}
	result.SquashedCommits = SquashRangeSummary{BaseHash: base, PersistHash: head}
	result.SquashedHash = head
	if base == head {
		return result, nil
	}
	if err := common.EnsureGitIdentity(ctx, input.RepoPath); err != nil {
		return result, err
	}
	details, err := collectCommitDetails(ctx, input.RepoPath, base, head)
	if err != nil {
		return result, err
	}
	author, message := buildSquashCommit(SquashRebaseMergeInput{Author: input.Author, CommitMessage: input.CommitMessage}, details)
	candidate, err := commitTree(ctx, input.RepoPath, head, base, author, message)
	if err != nil {
		return result, err
	}
	if _, err := common.ExecuteGitCommand(ctx, input.RepoPath, "reset", "--hard", candidate); err != nil {
		return result, err
	}
	// Keep the existing workspace base: this candidate is still unpublished and
	// must be carried by the task's persisted snapshot.
	result.SquashedHash = candidate
	return result, nil
}

func executeMerge(inv ops.OpDependencies, ctx context.Context, input MergeInput) (MergeOutput, error) {
	source := input.UpstreamRepo
	if inv != nil && inv.WorktreePath() == input.RepoPath {
		source = inv.GitContext().BaseRepo
	}
	result, err := mergeFrom(ctx, input, managedEmptyBase(inv, input.RepoPath), source)
	if err != nil {
		return result, err
	}
	return result, setPublishedBase(inv, input, result.MergedHash)
}

func setPublishedBase(inv ops.OpDependencies, input MergeInput, hash string) error {
	if inv != nil && inv.WorktreePath() == input.RepoPath {
		g := inv.GitContext()
		// Publishing to a different destination does not make this commit
		// available from the workspace's source. Retain its snapshot there.
		if normalizeRemoteURL(input.UpstreamRepo) == normalizeRemoteURL(g.BaseRepo) &&
			strings.TrimPrefix(input.UpstreamBranch, "refs/heads/") == strings.TrimPrefix(g.BaseRef, "refs/heads/") {
			return ops.SetWorkspaceBase(inv, hash)
		}
	}
	return nil
}

func merge(ctx context.Context, input MergeInput, emptyBase bool) (MergeOutput, error) {
	return mergeFrom(ctx, input, emptyBase, input.UpstreamRepo)
}

func mergeFrom(ctx context.Context, input MergeInput, emptyBase bool, source string) (MergeOutput, error) {
	var result MergeOutput
	if input.UpstreamRepo == "" || input.UpstreamBranch == "" {
		return result, fmt.Errorf("upstream_repo and upstream_branch are required")
	}
	if err := requireCleanWorkspace(ctx, input.RepoPath); err != nil {
		return result, err
	}
	branch := "refs/heads/" + strings.TrimPrefix(input.UpstreamBranch, "refs/heads/")
	if _, err := common.ExecuteGitCommand(ctx, input.RepoPath, "check-ref-format", branch); err != nil {
		return result, err
	}
	head, err := common.GetCommitHash(ctx, input.RepoPath, "HEAD")
	if err != nil {
		return result, err
	}
	args := []string{"push"}
	if emptyBase {
		// Receive-pack can create an empty repository on demand. Do not fetch
		// first; insist the branch is absent (or already at this exact commit).
		args = append(args, "--force-with-lease="+branch+":")
	} else {
		published, err := verifyMergeAncestry(ctx, input.RepoPath, source, input.UpstreamRepo, branch, head)
		if err != nil {
			return result, err
		}
		if published {
			return MergeOutput{MergedHash: head, TargetBranch: branch}, nil
		}
		// Ordinary push enforces fast-forward on the server, including a race
		// with another publisher. Never force or rewrite the prepared commit.
		args = append(args, "--no-force")
	}
	args = append(args, "--", input.UpstreamRepo, head+":"+branch)
	if _, err := common.ExecuteGitCommand(ctx, input.RepoPath, args...); err != nil {
		// A rejected/lost response may follow successful publication. This also
		// handles an initial publication followed by another writer's commit.
		// Initial creation must reach receive-pack before any read-side lookup.
		if published, checkErr := publicationPresent(ctx, input.RepoPath, input.UpstreamRepo, branch, head); checkErr == nil && published {
			return MergeOutput{MergedHash: head, TargetBranch: branch}, nil
		}
		if emptyBase {
			return result, fmt.Errorf("publish initial branch %s (destination must be absent or contain the saved candidate): %w", branch, err)
		}
		return result, fmt.Errorf("publish prepared commit to %s: %w", branch, err)
	}
	return MergeOutput{MergedHash: head, TargetBranch: branch}, nil
}

func requireCleanWorkspace(ctx context.Context, path string) error {
	if err := common.ValidateRepository(path); err != nil {
		return err
	}
	status, err := common.ExecuteGitCommand(ctx, path, "status", "--porcelain")
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return fmt.Errorf("workspace must be clean; commit changes before this operation")
	}
	return nil
}
