package gitcommit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/git/pkg/common"
)

// PersistCommit performs the Git commit and thin pack generation
func PersistCommit(ctx context.Context, input PersistCommitActivity) (*PersistCommitOutput, error) {
	// Set default timeout if not provided
	if input.Timeout == 0 {
		input.Timeout = 5 * time.Minute
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, input.Timeout)
	defer cancel()

	// Validate repository
	if err := common.ValidateRepository(input.RepoPath); err != nil {
		return nil, fmt.Errorf("invalid repository: %w", err)
	}

	// Validate storage location
	if err := os.MkdirAll(input.StorageLocation, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage location: %w", err)
	}

	// Validate root hash exists in repository
	if !common.CommitExists(ctx, input.RepoPath, input.RootHash) {
		return nil, fmt.Errorf("root hash %s does not exist in repository", input.RootHash)
	}

	// Get initial commit hash before any changes
	initialCommit, err := common.GetCommitHash(ctx, input.RepoPath, "HEAD")
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
	hasChanges := len(strings.TrimSpace(string(statusOutput))) > 0

	if hasChanges {
		// There are changes to commit
		// Stage all changes (both untracked and modified)
		_, err = common.ExecuteGitCommand(ctx, input.RepoPath, "add", "-A")
		if err != nil {
			// If staging fails, it might be because everything is already staged
			// Continue anyway
		}

		// Configure git user if author is provided
		if input.Author != "" {
			// Parse author string (format: "Name <email>")
			parts := strings.Split(input.Author, " <")
			if len(parts) == 2 {
				name := parts[0]
				email := strings.TrimSuffix(parts[1], ">")

				_, err = common.ExecuteGitCommand(ctx, input.RepoPath, "config", "user.name", name)
				if err != nil {
					return nil, fmt.Errorf("failed to set git user name: %w", err)
				}

				_, err = common.ExecuteGitCommand(ctx, input.RepoPath, "config", "user.email", email)
				if err != nil {
					return nil, fmt.Errorf("failed to set git user email: %w", err)
				}
			}
		}

		// Create commit
		commitMessage := input.CommitMessage
		if commitMessage == "" {
			commitMessage = "Automated commit from PersistCommit activity"
		}

		_, err = common.ExecuteGitCommand(ctx, input.RepoPath, "commit", "-m", commitMessage)
		if err != nil {
			return nil, fmt.Errorf("failed to create commit: %w", err)
		}

		// Get the new commit hash
		newCommitHash, err := common.GetCommitHash(ctx, input.RepoPath, "HEAD")
		if err != nil {
			return nil, fmt.Errorf("failed to get commit hash: %w", err)
		}
		commitHash = newCommitHash
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
			commitHash[:7],
			parentHash[:7],
			input.RootHash[:7])
		packPath = filepath.Join(input.StorageLocation, packName)

		// Create bundle containing commits from root to current
		// This ensures the bundle can be applied when only root is available
		_, err = common.ExecuteGitCommand(ctx, input.RepoPath,
			"bundle", "create", packPath,
			"HEAD", fmt.Sprintf("^%s", input.RootHash))
		if err != nil {
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

	// Use context lines of 10 for wide context window
	contextLines := 10

	// Generate diff from parent hash
	if persistOutput.ParentHash != "" {
		diffFromParentInput := GenerateDiffInput{
			RepoPath:        input.RepoPath,
			FromHash:        persistOutput.ParentHash,
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
	if baseHash != "" && baseHash != persistOutput.ParentHash {
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
