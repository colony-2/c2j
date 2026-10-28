package squashrebasemerge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// MergeUpstreamAdvancedCode identifies a prepared candidate that needs rebasing.
// Recovery policy belongs to the recipe's catch clause.
const MergeUpstreamAdvancedCode = "MERGE_UPSTREAM_ADVANCED"

func classifyMergeAdvancement(ctx context.Context, input MergeInput, source, phase string, err error) error {
	var divergence common.NotFastForwardError
	if input.PreparedBaseHash == "" || !errors.As(err, &divergence) {
		return err
	}
	base, resolveErr := common.ExecuteGitCommand(ctx, input.RepoPath, "rev-parse", "--verify", "--end-of-options", input.PreparedBaseHash+"^{commit}")
	if resolveErr != nil {
		return fmt.Errorf("resolve prepared merge base: %w", resolveErr)
	}
	baseHash := strings.TrimSpace(string(base))
	if baseHash == divergence.UpstreamHead {
		return err
	}
	for _, side := range []struct{ remote, head string }{
		{source, divergence.LocalHead}, {input.UpstreamRepo, divergence.UpstreamHead},
	} {
		ok, ancestryErr := hasAncestor(ctx, input.RepoPath, side.remote, baseHash, side.head)
		if ancestryErr != nil {
			return fmt.Errorf("verify prepared merge base: %w", ancestryErr)
		}
		if !ok {
			return err
		}
	}
	return &jobdb.AppError{Payload: jobdb.AppErrorPayload{
		Message: "Target branch advanced beyond the prepared base; rebase and review again.",
		Attrs: map[string]any{
			"code":               MergeUpstreamAdvancedCode,
			"candidate_hash":     divergence.LocalHead,
			"prepared_base_hash": baseHash,
			"upstream_hash":      divergence.UpstreamHead,
			"target_branch":      divergence.TargetBranch,
			"phase":              phase,
		},
	}}
}

func isMergeAdvancement(err error) bool {
	var app *jobdb.AppError
	return errors.As(err, &app) && app.Payload.Attrs["code"] == MergeUpstreamAdvancedCode
}

// Keep porcelain stdout separate from stderr: hook messages are not ref status.
// A fixed locale makes Git's finite set of rejection reasons comparable.
func pushMergeCandidate(ctx context.Context, repo string, args []string) (string, string, error) {
	cmd := gitprofile.Command(ctx, args...)
	cmd.Dir = repo
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := gitprofile.Run(ctx, cmd)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			err = errors.Join(err, cause)
		}
		err = fmt.Errorf("git push failed: %w (stdout: %s; stderr: %s)", err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String(), err
}

var refUpdateRace = regexp.MustCompile(`cannot lock ref '([^']+)': is at ([0-9a-f]{40,64}) but expected ([0-9a-f]{40,64})`)

func pushRejectedForAdvancement(stdout, stderr, candidate, branch string) bool {
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[0] != "!" || fields[1] != candidate+":"+branch {
			continue
		}
		switch fields[2] {
		case "[rejected] (non-fast-forward)", "[rejected] (fetch first)", "[remote rejected] (non-fast-forward)":
			return true
		case "[remote rejected] (failed to update ref)":
			// This status also covers permissions and lock failures. Only an
			// explicit old-value mismatch establishes a concurrent ref update.
			for _, match := range refUpdateRace.FindAllStringSubmatch(stderr, -1) {
				if match[1] == branch && match[2] != match[3] {
					return true
				}
			}
		}
	}
	return false
}
