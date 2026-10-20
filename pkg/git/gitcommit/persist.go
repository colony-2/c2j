package gitcommit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/git/common"
)

// PersistCommit performs the Git commit and thin pack generation
func PersistCommit(ctx context.Context, input PersistCommitActivity) (*PersistCommitOutput, error) {
	if input.Timeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, input.Timeout)
		defer cancel()
	}

	// Validate repository
	if err := common.ValidateRepository(input.RepoPath); err != nil {
		return nil, fmt.Errorf("invalid repository: %w", err)
	}

	// Validate storage location
	if err := os.MkdirAll(input.StorageLocation, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage location: %w", err)
	}

	// Validate root hash exists in repository
	if !input.EmptyBase && !common.CommitExists(ctx, input.RepoPath, input.RootHash) {
		return nil, fmt.Errorf("root hash %s does not exist in repository", input.RootHash)
	}

	// Get initial commit hash before any changes
	initialCommit, err := common.HeadCommit(ctx, input.RepoPath, input.EmptyBase)
	if err != nil {
		return nil, fmt.Errorf("failed to get initial commit: %w", err)
	}
	parentHash := initialCommit

	// Check if there are changes (staged or unstaged)
	statusOutput, err := common.ExecuteGitCommand(ctx, input.RepoPath, "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("failed to check git status: %w", err)
	}

	var commitHash string
	dirtyChanges := len(strings.TrimSpace(string(statusOutput))) > 0
	historyAdvanced := initialCommit != "" && ((input.EmptyBase && input.ExpectedHeadHash == "") || (input.ExpectedHeadHash != "" && !hashMatches(initialCommit, input.ExpectedHeadHash)))
	hasChanges := dirtyChanges || historyAdvanced

	if dirtyChanges {
		// There are changes to commit
		// Stage all changes (both untracked and modified)
		_, err = common.ExecuteGitCommand(ctx, input.RepoPath, "add", "-A")
		if err != nil {
			// If staging fails, it might be because everything is already staged
			// Continue anyway
		}

		if err := common.EnsureGitIdentity(ctx, input.RepoPath); err != nil {
			return nil, err
		}

		// Create commit
		commitMessage := input.CommitMessage
		if commitMessage == "" {
			commitMessage = "Automated commit from PersistCommit activity"
		}

		args := []string{"commit", "-m", commitMessage}
		if input.Author != "" {
			args = append(args, "--author", input.Author)
		}
		_, err = common.ExecuteGitCommand(ctx, input.RepoPath, args...)
		if err != nil {
			return nil, fmt.Errorf("failed to create commit: %w", err)
		}

		// Get the new commit hash
		newCommitHash, err := common.GetCommitHash(ctx, input.RepoPath, "HEAD")
		if err != nil {
			return nil, fmt.Errorf("failed to get commit hash: %w", err)
		}
		commitHash = newCommitHash
	} else if historyAdvanced {
		// The operation advanced HEAD itself, so carry that history forward even
		// when the worktree is clean.
		commitHash = initialCommit
		parents, e := common.ExecuteGitCommand(ctx, input.RepoPath, "rev-list", "--parents", "-n", "1", "HEAD")
		err = e
		fields := strings.Fields(string(parents))
		parentHash = ""
		if len(fields) > 1 {
			parentHash = fields[1]
		}
		if err != nil {
			return nil, fmt.Errorf("failed to get parent hash for existing head %s: %w", shortHash(commitHash), err)
		}
	} else {
		// No changes to commit, use current HEAD
		commitHash = parentHash
	}

	var packPath string
	var packSize int64

	if hasChanges {
		// Generate thin pack
		// Format: {commit_hash}-{parent_hash}-{root_hash}.pack
		packName := fmt.Sprintf("%s-%s-%s.pack",
			shortHash(commitHash),
			shortHash(parentHash),
			shortHash(input.RootHash))
		if input.EmptyBase {
			packName = shortHash(commitHash) + "-empty-empty.pack"
		}
		packPath = filepath.Join(input.StorageLocation, packName)

		// Create bundle containing commits from root to current
		// This ensures the bundle can be applied when only root is available
		args := []string{"bundle", "create", packPath, "HEAD"}
		if !input.EmptyBase {
			args = append(args, "^"+input.RootHash)
		}
		_, err = common.ExecuteGitCommand(ctx, input.RepoPath, args...)
		if err != nil {
			if input.EmptyBase {
				return nil, fmt.Errorf("create self-contained snapshot: %w", err)
			}
			// If that fails (maybe root is HEAD), include just this commit
			_, err = common.ExecuteGitCommand(ctx, input.RepoPath,
				"bundle", "create", packPath,
				"-1", "HEAD")
			if err != nil {
				return nil, fmt.Errorf("failed to create thin pack for commit %s: %w",
					commitHash[:7], err)
			}
		}
		// Get pack file size
		fileInfo, err := os.Stat(packPath)
		if err != nil {
			return nil, fmt.Errorf("failed to stat thin pack: %w", err)
		}
		packSize = fileInfo.Size()
	}

	return &PersistCommitOutput{
		CommitHash:   commitHash,
		ParentHash:   parentHash,
		ThinPackPath: packPath,
		ThinPackSize: packSize,
		CreatedAt:    time.Now(),
		HasChanges:   hasChanges,
	}, nil
}

func hashMatches(actual, expected string) bool {
	actual = strings.TrimSpace(actual)
	expected = strings.TrimSpace(expected)
	if actual == "" || expected == "" {
		return false
	}
	if len(actual) > 7 {
		actual = actual[:7]
	}
	if len(expected) > 7 {
		expected = expected[:7]
	}
	return strings.EqualFold(actual, expected)
}

// PersistCommitWithDiffs performs commit, thin pack generation, and creates diffs from parent and base
func PersistCommitWithDiffs(ctx context.Context, input PersistCommitActivity, baseHash string) (*PersistWithDiffsOutput, error) {
	// First, perform the regular persist operation
	persistOutput, err := PersistCommit(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("persist commit failed: %w", err)
	}

	// Initialize result with persist output
	result := &PersistWithDiffsOutput{
		PersistCommitOutput: *persistOutput,
	}

	// Only generate diffs if there were changes
	if !persistOutput.HasChanges {
		return result, nil
	}

	// A root commit is diffed against the empty tree, never a fake commit.
	parentHash := persistOutput.ParentHash
	if input.EmptyBase {
		out, err := common.ExecuteGitCommand(ctx, input.RepoPath, "hash-object", "-w", "-t", "tree", "--stdin")
		if err != nil {
			return nil, err
		}
		baseHash = strings.TrimSpace(string(out))
		if parentHash == "" {
			parentHash = baseHash
		}
	}

	// Use context lines of 10 for wide context window
	contextLines := 10

	// Generate diff from parent hash
	if parentHash != "" {
		diffFromParentInput := GenerateDiffInput{
			RepoPath:        input.RepoPath,
			FromHash:        parentHash,
			ToHash:          persistOutput.CommitHash,
			StorageLocation: input.StorageLocation,
			ContextLines:    contextLines,
		}
		diffFromParentOutput, err := GenerateDiff(ctx, diffFromParentInput)
		if err != nil {
			return nil, fmt.Errorf("failed to generate diff from parent: %w", err)
		}
		result.DiffFromParentPath = diffFromParentOutput.DiffPath
		result.DiffFromParentSize = diffFromParentOutput.DiffSize
	}

	// Generate diff from base hash (if provided and different from parent)
	if baseHash != "" && baseHash != parentHash {
		diffFromBaseInput := GenerateDiffInput{
			RepoPath:        input.RepoPath,
			FromHash:        baseHash,
			ToHash:          persistOutput.CommitHash,
			StorageLocation: input.StorageLocation,
			ContextLines:    contextLines,
		}
		diffFromBaseOutput, err := GenerateDiff(ctx, diffFromBaseInput)
		if err != nil {
			return nil, fmt.Errorf("failed to generate diff from base: %w", err)
		}
		result.DiffFromBasePath = diffFromBaseOutput.DiffPath
		result.DiffFromBaseSize = diffFromBaseOutput.DiffSize
	}

	return result, nil
}
