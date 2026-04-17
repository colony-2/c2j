package gitshallow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/colony-2/c2j/pkg/git/common"
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
	// Validate inputs
	if input.SourceDir == "" {
		return nil, fmt.Errorf("source directory cannot be empty")
	}
	if input.TargetDir == "" {
		return nil, fmt.Errorf("target directory cannot be empty")
	}
	if input.CommitHash == "" {
		return nil, fmt.Errorf("commit hash cannot be empty")
	}

	// For local paths ensure the source is a git repository; remote URLs are validated by git clone itself.
	if !common.IsRemoteRepository(input.SourceDir) {
		if err := common.ValidateRepository(input.SourceDir); err != nil {
			return nil, fmt.Errorf("source directory validation failed: %w", err)
		}
	}

	if info, err := os.Stat(input.TargetDir); err == nil {
		// Exists but not a directory
		if !info.IsDir() {
			return nil, fmt.Errorf("target path exists but is not a directory: %s", input.TargetDir)
		}

		// Exists and is a directory; check if non-empty
		entries, readErr := os.ReadDir(input.TargetDir)
		if readErr != nil {
			return nil, fmt.Errorf("failed to read target directory: %w", readErr)
		}

		if len(entries) > 0 {
			return nil, fmt.Errorf("target directory already exists and is not empty: %s", input.TargetDir)
		}
	}

	// Create parent directory if needed
	parentDir := filepath.Dir(input.TargetDir)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create parent directory: %w", err)
	}

	// Perform shallow clone with depth 1
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--no-single-branch", input.SourceDir, input.TargetDir)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to clone repository: %w", err)
	}

	// Fetch the specific commit
	_, err := common.ExecuteGitCommand(ctx, input.TargetDir, "fetch", "--depth", "1", "origin", input.CommitHash)
	if err != nil {
		// Clean up on failure
		os.RemoveAll(input.TargetDir)
		return nil, fmt.Errorf("failed to fetch commit %s: %w", input.CommitHash, err)
	}

	// Checkout the specific commit
	_, err = common.ExecuteGitCommand(ctx, input.TargetDir, "checkout", input.CommitHash)
	if err != nil {
		// Clean up on failure
		os.RemoveAll(input.TargetDir)
		return nil, fmt.Errorf("failed to checkout commit %s: %w", input.CommitHash, err)
	}

	return &GitShallowCloneOutput{
		ClonedPath: input.TargetDir,
	}, nil
}
