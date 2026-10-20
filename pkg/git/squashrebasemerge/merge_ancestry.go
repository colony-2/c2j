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

// verifyMergeAncestry deepens only the published boundaries behind the candidate.
// The candidate itself is unpublished and cannot be fetched from a remote.
func verifyMergeAncestry(ctx context.Context, repo, source, destination, branch, head string) error {
	refs, err := common.ExecuteGitCommand(ctx, repo, "ls-remote", "--exit-code", "--heads", "--", destination, branch)
	if err != nil {
		return fmt.Errorf("resolve merge target: %w", err)
	}
	fields := strings.Fields(string(refs))
	if len(fields) != 2 {
		return fmt.Errorf("expected one merge target for %s", branch)
	}
	target := fields[0]
	if target == head {
		return nil
	}
	if !common.CommitExists(ctx, repo, target) {
		if _, err := common.ExecuteGitCommand(ctx, repo, "fetch", "--no-tags", "--depth=1", "--", destination, branch); err != nil {
			return fmt.Errorf("fetch merge target: %w", err)
		}
	}
	previous := ""
	for depth := 2; depth <= 1048576; depth *= 2 {
		ok, err := isAncestor(ctx, repo, target, head)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		path, err := common.ExecuteGitCommand(ctx, repo, "rev-parse", "--git-path", "shallow")
		if err != nil {
			return err
		}
		name := strings.TrimSpace(string(path))
		if !filepath.IsAbs(name) {
			name = filepath.Join(repo, name)
		}
		data, err := os.ReadFile(name)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		boundaries := map[string]bool{}
		for _, hash := range strings.Fields(string(data)) {
			boundaries[hash] = true
		}
		roots, err := common.ExecuteGitCommand(ctx, repo, "rev-list", "--max-parents=0", head)
		if err != nil {
			return err
		}
		var missing []string
		for _, hash := range strings.Fields(string(roots)) {
			if boundaries[hash] {
				missing = append(missing, hash)
			}
		}
		if len(missing) == 0 {
			return common.NotFastForwardError{TargetBranch: branch, LocalHead: head, UpstreamHead: target}
		}
		boundary := strings.Join(missing, " ")
		if boundary == previous {
			return fmt.Errorf("cannot verify fast-forward: source has incomplete shallow history")
		}
		previous = boundary
		for _, hash := range missing {
			if _, err := common.ExecuteGitCommand(ctx, repo, "fetch", "--no-tags", "--depth="+strconv.Itoa(depth), "--", source, hash); err != nil {
				return fmt.Errorf("fetch merge ancestry: %w", err)
			}
		}
	}
	return fmt.Errorf("cannot verify fast-forward: ancestry depth limit reached")
}
