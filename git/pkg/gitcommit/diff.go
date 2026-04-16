package gitcommit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/colony-2/c2j/git/pkg/common"
)

// GenerateDiffInput defines parameters for generating git diffs
type GenerateDiffInput struct {
	RepoPath        string // Path to the repository
	FromHash        string // Source commit hash
	ToHash          string // Target commit hash (usually HEAD)
	StorageLocation string // Directory where diff will be saved
	ContextLines    int    // Number of context lines (before/after)
}

// GenerateDiffOutput represents the result of diff generation
type GenerateDiffOutput struct {
	DiffPath string // Path to generated diff file
	DiffSize int64  // Size of diff file in bytes
}

// GenerateDiff creates a diff between two commits with the specified context
func GenerateDiff(ctx context.Context, input GenerateDiffInput) (*GenerateDiffOutput, error) {
	// Validate inputs
	if err := common.ValidateRepository(input.RepoPath); err != nil {
		return nil, fmt.Errorf("invalid repository: %w", err)
	}

	if input.FromHash == "" || input.ToHash == "" {
		return nil, fmt.Errorf("both from_hash and to_hash are required")
	}

	// Validate storage location
	if err := os.MkdirAll(input.StorageLocation, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage location: %w", err)
	}

	// Set default context lines if not provided (wide window for better context)
	contextLines := input.ContextLines
	if contextLines == 0 {
		contextLines = 10 // Wide context window
	}

	// Verify both commits exist
	if !common.CommitExists(ctx, input.RepoPath, input.FromHash) {
		return nil, fmt.Errorf("from_hash %s does not exist in repository", input.FromHash)
	}
	if !common.CommitExists(ctx, input.RepoPath, input.ToHash) {
		return nil, fmt.Errorf("to_hash %s does not exist in repository", input.ToHash)
	}

	// Generate diff using git diff with unified context
	diffOutput, err := common.ExecuteGitCommand(ctx, input.RepoPath,
		"diff",
		fmt.Sprintf("--unified=%d", contextLines),
		input.FromHash,
		input.ToHash,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to generate diff: %w", err)
	}

	// Create diff filename: {from_hash}-{to_hash}.diff
	fromShort := shortHash(input.FromHash)
	toShort := shortHash(input.ToHash)
	diffFilename := fmt.Sprintf("%s-%s.diff", fromShort, toShort)
	diffPath := filepath.Join(input.StorageLocation, diffFilename)

	// Write diff to file
	if err := os.WriteFile(diffPath, diffOutput, 0644); err != nil {
		return nil, fmt.Errorf("failed to write diff file: %w", err)
	}

	// Get file size
	fileInfo, err := os.Stat(diffPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat diff file: %w", err)
	}

	return &GenerateDiffOutput{
		DiffPath: diffPath,
		DiffSize: fileInfo.Size(),
	}, nil
}

func shortHash(hash string) string {
	if len(hash) >= 7 {
		return hash[:7]
	}
	return hash
}
