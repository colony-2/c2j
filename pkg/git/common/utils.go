package common

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecuteGitCommand runs a git command with context
func ExecuteGitCommand(ctx context.Context, repoPath string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("git command failed: %w (output: %s)", err, string(output))
	}
	return output, nil
}

// ValidateRepository checks if path is within a valid Git repository
func ValidateRepository(repoPath string) error {
	if repoPath == "" {
		return fmt.Errorf("repository path cannot be empty")
	}

	// Use git rev-parse to find the git directory
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("not a git repository (or any of the parent directories): %s", repoPath)
	}

	// Verify we got a valid response
	gitDir := strings.TrimSpace(string(output))
	if gitDir == "" {
		return fmt.Errorf("could not determine git directory for: %s", repoPath)
	}

	return nil
}

// FindGitRoot returns the root directory of the git repository
func FindGitRoot(path string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = path
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("not in a git repository: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// ParseThinPackName extracts metadata from thin pack filename
func ParseThinPackName(filename string) (*ThinPackMetadata, error) {
	// Parse format: {commit_hash}-{parent_hash}-{root_hash}.pack
	base := strings.TrimSuffix(filepath.Base(filename), ".pack")
	parts := strings.Split(base, "-")

	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid thin pack filename format: %s", filename)
	}

	return &ThinPackMetadata{
		CommitHash: parts[0],
		ParentHash: parts[1],
		RootHash:   parts[2],
		FilePath:   filename,
	}, nil
}

// GetCommitHash returns the full commit hash for a given ref
func GetCommitHash(ctx context.Context, repoPath, ref string) (string, error) {
	output, err := ExecuteGitCommand(ctx, repoPath, "rev-parse", ref)
	if err != nil {
		return "", fmt.Errorf("failed to get commit hash for %s: %w", ref, err)
	}
	return strings.TrimSpace(string(output)), nil
}

// CommitExists checks if a commit exists in the repository
func CommitExists(ctx context.Context, repoPath, commitHash string) bool {
	_, err := ExecuteGitCommand(ctx, repoPath, "cat-file", "-e", commitHash)
	return err == nil
}

// IsRemoteRepository reports whether the provided source string looks like a git remote
// (e.g. ssh/https/file URLs or the SCP-like "git@host:org/repo.git" syntax).
func IsRemoteRepository(source string) bool {
	if strings.Contains(source, "://") {
		return true
	}
	return strings.HasPrefix(source, "git@")
}
