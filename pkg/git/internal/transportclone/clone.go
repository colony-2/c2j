package transportclone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/gitprofile"
)

type Policy uint8

const (
	RetainHistory Policy = iota
	DepthOne
)

func Clone(ctx context.Context, source, target, revision string, policy Policy) error {
	if source == "" {
		return fmt.Errorf("source directory cannot be empty")
	}
	if target == "" {
		return fmt.Errorf("target directory cannot be empty")
	}
	if revision == "" {
		return fmt.Errorf("commit hash cannot be empty")
	}

	// For local paths ensure the source is a git repository; remote URLs are validated by git clone itself.
	if !common.IsRemoteRepository(source) {
		if err := common.ValidateRepositoryContext(ctx, source); err != nil {
			return fmt.Errorf("source directory validation failed: %w", err)
		}
	}

	if info, err := os.Stat(target); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("target path exists but is not a directory: %s", target)
		}

		entries, readErr := os.ReadDir(target)
		if readErr != nil {
			return fmt.Errorf("failed to read target directory: %w", readErr)
		}

		if len(entries) > 0 {
			return fmt.Errorf("target directory already exists and is not empty: %s", target)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect target directory: %w", err)
	}

	parentDir := filepath.Dir(target)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	var depth []string
	if policy == DepthOne {
		depth = []string{"--depth", "1"}
	}
	cloneArgs := append([]string{"clone", "--no-local"}, depth...)
	cmd := gitprofile.Command(ctx, append(cloneArgs, "--no-single-branch", source, target)...)
	if output, err := gitprofile.CombinedOutput(ctx, cmd); err != nil {
		failure := fmt.Errorf("failed to clone repository: %w (output: %s)", err, output)
		if policy == RetainHistory {
			if cause := context.Cause(ctx); cause != nil {
				failure = errors.Join(failure, cause)
			}
			return cleanFailure(target, failure)
		}
		return failure
	}

	fetchArgs := append([]string{"fetch"}, depth...)
	if _, err := common.ExecuteGitCommand(ctx, target, append(fetchArgs, "origin", revision)...); err != nil {
		return cleanFailure(target, fmt.Errorf("failed to fetch commit %s: %w", revision, err))
	}
	if _, err := common.ExecuteGitCommand(ctx, target, "checkout", revision); err != nil {
		return cleanFailure(target, fmt.Errorf("failed to checkout commit %s: %w", revision, err))
	}
	return nil
}

func cleanFailure(target string, failure error) error {
	if err := os.RemoveAll(target); err != nil {
		return errors.Join(failure, fmt.Errorf("clean incomplete clone: %w", err))
	}
	return failure
}
