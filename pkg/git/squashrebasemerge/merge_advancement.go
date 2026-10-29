package squashrebasemerge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// Push output is retained for diagnostics; classification uses repository state.
func pushMergeCandidate(ctx context.Context, repo string, args []string) error {
	cmd := gitprofile.Command(ctx, args...)
	cmd.Dir = repo
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := gitprofile.Run(ctx, cmd)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			err = errors.Join(err, cause)
		}
		err = fmt.Errorf("git push failed: %w (stdout: %s; stderr: %s)", err, stdout.String(), stderr.String())
	}
	return err
}
