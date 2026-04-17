package gitcommit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/git/common"
)

// RestoreCommit restores repository to a specific commit state
func RestoreCommit(ctx context.Context, input RestoreCommitActivity) (*RestoreCommitOutput, error) {
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

	// Check if we have uncommitted changes and force flag is not set
	if !input.Force {
		statusOutput, err := common.ExecuteGitCommand(ctx, input.RepoPath, "status", "--porcelain")
		if err != nil {
			return nil, fmt.Errorf("failed to check git status: %w", err)
		}

		if len(statusOutput) > 0 {
			return nil, fmt.Errorf("uncommitted changes in repository, use force flag to override")
		}
	}

	// Check if target commit exists in repository
	if common.CommitExists(ctx, input.RepoPath, input.TargetCommit) {
		// Commit exists, just checkout
		checkoutArgs := []string{"checkout"}
		if input.Force {
			checkoutArgs = append(checkoutArgs, "-f")
		}
		checkoutArgs = append(checkoutArgs, input.TargetCommit)

		_, err := common.ExecuteGitCommand(ctx, input.RepoPath, checkoutArgs...)
		if err != nil {
			return nil, fmt.Errorf("failed to checkout commit: %w", err)
		}

		// Get current commit to verify
		currentCommit, err := common.GetCommitHash(ctx, input.RepoPath, "HEAD")
		if err != nil {
			return nil, fmt.Errorf("failed to get current commit: %w", err)
		}

		return &RestoreCommitOutput{
			Success:       true,
			CurrentCommit: currentCommit,
			RestoredFrom:  "repository",
			RestoredAt:    time.Now(),
		}, nil
	}

	// Commit doesn't exist, need to restore from thin packs
	// First, verify root hash exists
	if !common.CommitExists(ctx, input.RepoPath, input.RootHash) {
		return nil, fmt.Errorf("root hash %s does not exist in repository", input.RootHash)
	}

	// Reset to root hash
	resetArgs := []string{"reset", "--hard", input.RootHash}
	_, err := common.ExecuteGitCommand(ctx, input.RepoPath, resetArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to reset to root hash: %w", err)
	}

	// Find and load thin packs
	packFiles, err := findThinPacks(input.StorageLocation, input.RootHash)
	if err != nil {
		return nil, fmt.Errorf("failed to find thin packs: %w", err)
	}

	// Build commit graph from pack files
	commitGraph := buildCommitGraph(packFiles)

	// Find path from root to target
	packChain, err := findPackChain(commitGraph, input.RootHash, input.TargetCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to find pack chain: %w", err)
	}

	// Apply thin packs in order
	appliedPacks := []string{}
	for _, packPath := range packChain {
		// Check if this is an empty marker file (no changes)
		fileInfo, err := os.Stat(packPath)
		if err != nil {
			return nil, fmt.Errorf("failed to stat pack file %s: %w", packPath, err)
		}

		if fileInfo.Size() == 0 {
			// Empty file means no changes, skip
			appliedPacks = append(appliedPacks, packPath)
			continue
		}

		// Apply the bundle
		// First, unbundle to get the commits into the repository
		_, err = common.ExecuteGitCommand(ctx, input.RepoPath, "bundle", "unbundle", packPath)
		if err != nil {
			// unbundle might not be available, try fetch instead
			_, err = common.ExecuteGitCommand(ctx, input.RepoPath, "fetch", packPath, "HEAD")
			if err != nil {
				return nil, fmt.Errorf("failed to apply thin pack %s: %w", packPath, err)
			}
		}

		appliedPacks = append(appliedPacks, packPath)
	}

	// After applying all packs, checkout the target commit
	if len(appliedPacks) > 0 {
		_, err := common.ExecuteGitCommand(ctx, input.RepoPath, "checkout", input.TargetCommit)
		if err != nil {
			return nil, fmt.Errorf("failed to checkout target commit after applying packs: %w", err)
		}
	}

	// Verify we reached the target commit
	currentCommit, err := common.GetCommitHash(ctx, input.RepoPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to get current commit after restore: %w", err)
	}

	// Check if current commit matches target (comparing first 7 chars for short hash compatibility)
	if !strings.HasPrefix(currentCommit, input.TargetCommit[:min(7, len(input.TargetCommit))]) &&
		!strings.HasPrefix(input.TargetCommit, currentCommit[:min(7, len(currentCommit))]) {
		return nil, fmt.Errorf("restore failed: current commit %s does not match target %s",
			currentCommit[:7], input.TargetCommit[:7])
	}

	return &RestoreCommitOutput{
		Success:          true,
		CurrentCommit:    currentCommit,
		RestoredFrom:     "thin_packs",
		ThinPacksApplied: appliedPacks,
		RestoredAt:       time.Now(),
	}, nil
}

// findThinPacks finds all thin pack files for the given root hash
func findThinPacks(storageLocation, rootHash string) ([]string, error) {
	pattern := filepath.Join(storageLocation, fmt.Sprintf("*-%s.pack", rootHash[:7]))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to find thin packs: %w", err)
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("no thin packs found for root hash %s", rootHash[:7])
	}

	return matches, nil
}

// buildCommitGraph builds a graph of commits from pack file names
func buildCommitGraph(packFiles []string) map[string]*common.ThinPackMetadata {
	graph := make(map[string]*common.ThinPackMetadata)

	for _, packFile := range packFiles {
		metadata, err := common.ParseThinPackName(packFile)
		if err != nil {
			continue // Skip invalid files
		}
		graph[metadata.CommitHash] = metadata
	}

	return graph
}

// findPackChain finds the chain of packs needed to go from root to target
func findPackChain(graph map[string]*common.ThinPackMetadata, rootHash, targetCommit string) ([]string, error) {
	// Find the target in the graph
	targetShort := targetCommit[:min(7, len(targetCommit))]

	var targetMetadata *common.ThinPackMetadata
	for commitHash, metadata := range graph {
		if strings.HasPrefix(commitHash, targetShort) || strings.HasPrefix(targetShort, commitHash) {
			targetMetadata = metadata
			break
		}
	}

	if targetMetadata == nil {
		return nil, fmt.Errorf("target commit %s not found in thin packs", targetShort)
	}

	// Build chain from root to target
	chain := []string{}
	current := targetMetadata

	for current != nil && current.CommitHash != rootHash[:7] {
		chain = append([]string{current.FilePath}, chain...) // Prepend to maintain order

		// Find parent in graph
		var parent *common.ThinPackMetadata
		for _, metadata := range graph {
			if metadata.CommitHash == current.ParentHash {
				parent = metadata
				break
			}
		}

		// If we can't find parent but we've reached a commit that has root as parent, we're done
		if parent == nil && current.ParentHash == rootHash[:7] {
			break
		}

		current = parent
	}

	if len(chain) == 0 {
		return nil, fmt.Errorf("could not build chain from root to target")
	}

	// Sort chain to ensure correct order (parent before child)
	sort.Slice(chain, func(i, j int) bool {
		metaI, _ := common.ParseThinPackName(chain[i])
		metaJ, _ := common.ParseThinPackName(chain[j])

		// If j's parent is i's commit, i should come before j
		return metaJ.ParentHash == metaI.CommitHash
	})

	return chain, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
