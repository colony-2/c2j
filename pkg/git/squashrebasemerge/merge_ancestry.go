package squashrebasemerge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/pkg/git/common"
)

// verifyMergeAncestry distinguishes an already-published candidate from a
// candidate that can still be pushed. Neither case permits rewriting history.
func verifyMergeAncestry(ctx context.Context, repo, source, destination, branch, head string) (bool, error) {
	target, err := fetchMergeTarget(ctx, repo, destination, branch)
	if err != nil {
		return false, err
	}
	if target == head {
		return true, nil
	}
	// The normal case is a prepared child of the destination. Prove that
	// first, rather than deepening the destination's entire history looking
	// for a commit which has not been published yet.
	ok, err := hasAncestor(ctx, repo, source, target, head)
	if err != nil {
		return false, err
	}
	if ok {
		return false, nil
	}
	published, err := hasAncestor(ctx, repo, destination, head, target)
	if err != nil || published {
		return published, err
	}
	return false, common.NotFastForwardError{TargetBranch: branch, LocalHead: head, UpstreamHead: target}
}

func publicationPresent(ctx context.Context, repo, destination, branch, head string) (bool, error) {
	target, err := fetchMergeTarget(ctx, repo, destination, branch)
	if err != nil {
		return false, err
	}
	return hasAncestor(ctx, repo, destination, head, target)
}

func fetchMergeTarget(ctx context.Context, repo, destination, branch string) (string, error) {
	refs, err := common.ExecuteGitCommand(ctx, repo, "ls-remote", "--exit-code", "--heads", "--", destination, branch)
	if err != nil {
		return "", fmt.Errorf("resolve merge target: %w", err)
	}
	fields := strings.Fields(string(refs))
	if len(fields) != 2 {
		return "", fmt.Errorf("expected one merge target for %s", branch)
	}
	target := fields[0]
	if !common.CommitExists(ctx, repo, target) {
		if _, err := common.ExecuteGitCommand(ctx, repo, "fetch", "--no-tags", "--depth=1", "--", destination, target); err != nil {
			return "", fmt.Errorf("fetch merge target: %w", err)
		}
	}
	return target, nil
}

// Deepen only published shallow boundaries, never an unpublished candidate.
func hasAncestor(ctx context.Context, repo, source, ancestor, descendant string) (bool, error) {
	previous := ""
	for depth := 2; depth <= 1048576; depth *= 2 {
		ok, err := isAncestor(ctx, repo, ancestor, descendant)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
		path, err := common.ExecuteGitCommand(ctx, repo, "rev-parse", "--git-path", "shallow")
		if err != nil {
			return false, err
		}
		name := strings.TrimSpace(string(path))
		if !filepath.IsAbs(name) {
			name = filepath.Join(repo, name)
		}
		data, err := os.ReadFile(name)
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
		boundaries := map[string]bool{}
		for _, hash := range strings.Fields(string(data)) {
			boundaries[hash] = true
		}
		roots, err := common.ExecuteGitCommand(ctx, repo, "rev-list", "--max-parents=0", descendant)
		if err != nil {
			return false, err
		}
		var missing []string
		for _, hash := range strings.Fields(string(roots)) {
			if boundaries[hash] {
				missing = append(missing, hash)
			}
		}
		if len(missing) == 0 {
			return false, nil
		}
		boundary := strings.Join(missing, " ")
		if boundary == previous {
			return false, fmt.Errorf("cannot verify fast-forward: source has incomplete shallow history")
		}
		previous = boundary
		for _, hash := range missing {
			if _, err := common.ExecuteGitCommand(ctx, repo, "fetch", "--no-tags", "--depth="+strconv.Itoa(depth), "--", source, hash); err != nil {
				return false, fmt.Errorf("fetch merge ancestry: %w", err)
			}
		}
	}
	return false, fmt.Errorf("cannot verify fast-forward: ancestry depth limit reached")
}
