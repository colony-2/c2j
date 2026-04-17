package common

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// normalizeScope ensures the pathspec is repo-relative using forward slashes.
func normalizeScope(scope string) (string, error) {
	trimmed := strings.TrimSpace(scope)
	if trimmed == "" {
		return "", fmt.Errorf("scope path cannot be empty")
	}
	cleaned := filepath.ToSlash(trimmed)
	for strings.HasPrefix(cleaned, "./") {
		cleaned = strings.TrimPrefix(cleaned, "./")
	}
	cleaned = strings.Trim(cleaned, "/")
	if cleaned == "" {
		return ".", nil
	}
	if cleaned == "." {
		return ".", nil
	}
	return cleaned, nil
}

// PrepareScopedCommit resets staged changes outside the provided scope, cleans untracked
// files, and stages the scoped path. It returns true when the scope produces staged changes.
func PrepareScopedCommit(ctx context.Context, repoPath, scope string) (bool, error) {
	normalized, err := normalizeScope(scope)
	if err != nil {
		return false, err
	}

	if normalized == "." {
		output, err := ExecuteGitCommand(ctx, repoPath, "status", "--porcelain")
		if err != nil {
			return false, fmt.Errorf("inspect git status: %w", err)
		}
		return strings.TrimSpace(string(output)) != "", nil
	}

	// Reset staging area
	if _, err := ExecuteGitCommand(ctx, repoPath, "reset", "--mixed"); err != nil {
		return false, fmt.Errorf("reset staging area: %w", err)
	}

	// Use git's pathspec exclusion to handle files outside scope
	// ":^<path>" means "exclude this path"
	exclude := fmt.Sprintf(":^%s", normalized)

	// IMPORTANT: Add scope files FIRST, before cleaning
	// Once files are staged, git clean won't remove them
	if _, err := ExecuteGitCommand(ctx, repoPath, "add", "-A", "--", normalized); err != nil {
		// It's ok if the path doesn't exist or has no files - just continue
		if !strings.Contains(err.Error(), "did not match any files") {
			return false, fmt.Errorf("stage changes in scope: %w", err)
		}
	}

	// Restore (revert and unstage) tracked files outside the scope
	// This will fail if there are no tracked files outside scope, which is fine
	_, _ = ExecuteGitCommand(ctx, repoPath, "restore", "--staged", "--worktree", "--", exclude)

	// Clean untracked files outside the scope
	// Now that scope files are staged, this won't remove them
	// This will fail if there are no untracked files outside scope, which is fine
	_, _ = ExecuteGitCommand(ctx, repoPath, "clean", "-fd", "--", exclude)

	diffOutput, err := ExecuteGitCommand(ctx, repoPath, "diff", "--cached", "--name-only")
	if err != nil {
		return false, fmt.Errorf("inspect staged diff: %w", err)
	}

	hasChanges := strings.TrimSpace(string(diffOutput)) != ""
	if !hasChanges {
		statusOutput, err := ExecuteGitCommand(ctx, repoPath, "status", "--porcelain")
		if err != nil {
			return false, fmt.Errorf("final status check: %w", err)
		}
		if residual := strings.TrimSpace(string(statusOutput)); residual != "" {
			return false, fmt.Errorf("residual changes after scoping: %s", residual)
		}
	}

	return hasChanges, nil
}

// EnsureCleanAfterRestore enforces a clean worktree after a restore operation by aligning the
// checkout with the target commit and cleaning untracked files outside the scoped path.

func EnsureCleanAfterRestore(ctx context.Context, repoPath, scope, targetCommit string) error {
	if targetCommit != "" {
		if _, err := ExecuteGitCommand(ctx, repoPath, "reset", "--hard", targetCommit); err != nil {
			return fmt.Errorf("reset to %s: %w", targetCommit, err)
		}
	} else {
		if _, err := ExecuteGitCommand(ctx, repoPath, "reset", "--hard"); err != nil {
			return fmt.Errorf("reset worktree: %w", err)
		}
	}

	if scope := strings.TrimSpace(scope); scope != "" && scope != "." {
		exclude := fmt.Sprintf(":^%s", scope)
		if _, err := ExecuteGitCommand(ctx, repoPath, "clean", "-fd", "--", exclude); err != nil {
			return fmt.Errorf("clean worktree outside scope: %w", err)
		}
	} else {
		if _, err := ExecuteGitCommand(ctx, repoPath, "clean", "-fd"); err != nil {
			return fmt.Errorf("clean worktree: %w", err)
		}
	}

	statusOutput, err := ExecuteGitCommand(ctx, repoPath, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("post-restore status: %w", err)
	}
	if residual := strings.TrimSpace(string(statusOutput)); residual != "" {
		return fmt.Errorf("worktree dirty after restore: %s", residual)
	}
	return nil
}
