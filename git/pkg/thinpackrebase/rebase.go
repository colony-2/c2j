package thinpackrebase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/git/pkg/common"
)

type workspaceSnapshot struct {
	RepoPath    string
	BaseHash    string
	PersistHash string
	BaseRepo    string
	GitAuthor   string
	Worktree    string
	CellName    string
}

// Run performs the thin-pack aware rebase and returns structured outputs.
func Run(ctx context.Context, input ThinpackRebaseInput) (*ThinpackRebaseOutput, error) {
	targetBase := strings.TrimSpace(input.TargetBaseHash)
	if targetBase == "" {
		return nil, fmt.Errorf("target_base_hash is required")
	}

	snapshot, err := resolveWorkspaceSnapshot(input)
	if err != nil {
		return nil, err
	}

	if err := common.ValidateRepository(snapshot.RepoPath); err != nil {
		return nil, fmt.Errorf("validate repository: %w", err)
	}

	if snapshot.BaseHash == "" {
		return nil, fmt.Errorf("base_hash is required for thinpackrebase")
	}

	if snapshot.PersistHash == "" {
		head, err := common.GetCommitHash(ctx, snapshot.RepoPath, "HEAD")
		if err != nil {
			return nil, fmt.Errorf("determine current HEAD: %w", err)
		}
		snapshot.PersistHash = head
	}

	remoteName, err := determineRemoteName(ctx, snapshot, input.UpstreamRemote)
	if err != nil {
		return nil, err
	}

	if err := ensureTargetAvailable(ctx, snapshot.RepoPath, remoteName, targetBase); err != nil {
		return nil, err
	}

	if err := ensureLinearHistory(ctx, snapshot.RepoPath, snapshot.BaseHash, targetBase); err != nil {
		return nil, err
	}

	rebasedFrom := RebasedFromSummary{BaseHash: snapshot.BaseHash, PersistHash: snapshot.PersistHash}

	equalBase := hashesEqual(snapshot.BaseHash, targetBase)

	var newHead string
	var updatedRef string

	commitCount, err := countCommitsSince(ctx, snapshot.RepoPath, snapshot.BaseHash)
	if err != nil {
		return nil, err
	}

	// Fast-forward when there are no commits to replay but the target is newer.
	if commitCount == 0 {
		// Nothing to reapply; align HEAD with the target base if needed.
		if !hashesEqual(snapshot.PersistHash, targetBase) {
			if _, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "reset", "--hard", targetBase); err != nil {
				return nil, fmt.Errorf("fast-forward to target base %s: %w", shortHash(targetBase), err)
			}
		}
	} else if !equalBase {
		if err := rebaseOntoTarget(ctx, snapshot, targetBase, input.PreserveAuthor); err != nil {
			return nil, err
		}
	}

	newHead, err = common.GetCommitHash(ctx, snapshot.RepoPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("determine rebased head: %w", err)
	}

	if strings.TrimSpace(input.UpdateRefs) != "" {
		ref := strings.TrimSpace(input.UpdateRefs)
		if _, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "update-ref", ref, newHead); err != nil {
			return nil, fmt.Errorf("update ref %s: %w", ref, err)
		}
		updatedRef = ref
	}

	output := &ThinpackRebaseOutput{
		TargetBaseHash: targetBase,
		NewBaseHash:    targetBase,
		NewPersistHash: newHead,
		UpdatedRef:     updatedRef,
		RebasedFrom:    rebasedFrom,
		GitContextPatch: map[string]interface{}{
			"base_hash":     targetBase,
			"persist_hash":  newHead,
			"previous_hash": rebasedFrom.PersistHash,
		},
	}

	return output, nil
}

func resolveWorkspaceSnapshot(input ThinpackRebaseInput) (workspaceSnapshot, error) {
	snapshot := workspaceSnapshot{}

	repoPath := strings.TrimSpace(input.RepoPath)
	if repoPath == "" {
		return snapshot, fmt.Errorf("repo_path is required")
	}

	snapshot.RepoPath = repoPath
	snapshot.Worktree = repoPath
	snapshot.BaseHash = strings.TrimSpace(input.BaseHash)
	snapshot.PersistHash = strings.TrimSpace(input.PersistHash)
	snapshot.BaseRepo = strings.TrimSpace(input.BaseRepo)
	snapshot.GitAuthor = strings.TrimSpace(input.GitAuthor)
	snapshot.CellName = strings.TrimSpace(input.CellName)

	if snapshot.GitAuthor == "" && snapshot.CellName != "" {
		snapshot.GitAuthor = fmt.Sprintf("%s <%s@colony2>", snapshot.CellName, snapshot.CellName)
	}

	return snapshot, nil
}

func determineRemoteName(ctx context.Context, snapshot workspaceSnapshot, explicit string) (string, error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit, nil
	}

	out, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "remote", "-v")
	if err != nil {
		// Repository might not have any remotes configured; treat as optional.
		return "", nil
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	baseNorm := normalizeRemoteURL(snapshot.BaseRepo)
	remoteSeen := make(map[string]struct{})
	var singleCandidate string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		url := fields[1]
		remoteSeen[name] = struct{}{}
		if singleCandidate == "" {
			singleCandidate = name
		}
		if baseNorm != "" && normalizeRemoteURL(url) == baseNorm {
			return name, nil
		}
	}

	if len(remoteSeen) == 1 {
		return singleCandidate, nil
	}

	return "", nil
}

func ensureTargetAvailable(ctx context.Context, repoPath, remoteName, target string) error {
	if common.CommitExists(ctx, repoPath, target) {
		return nil
	}
	if remoteName == "" {
		return fmt.Errorf("target base hash %s not found locally and no remote configured", shortHash(target))
	}

	if _, err := common.ExecuteGitCommand(ctx, repoPath, "fetch", remoteName); err != nil {
		return fmt.Errorf("fetch remote %s: %w", remoteName, err)
	}

	if common.CommitExists(ctx, repoPath, target) {
		return nil
	}
	return fmt.Errorf("target base hash %s not available after fetching %s", shortHash(target), remoteName)
}

func ensureLinearHistory(ctx context.Context, repoPath, currentBase, targetBase string) error {
	if hashesEqual(currentBase, targetBase) {
		return nil
	}

	ancestor, err := isAncestor(ctx, repoPath, currentBase, targetBase)
	if err != nil {
		return err
	}
	if !ancestor {
		return fmt.Errorf("target base %s does not descend from current base %s", shortHash(targetBase), shortHash(currentBase))
	}
	return nil
}

func isAncestor(ctx context.Context, repoPath, ancestor, descendant string) (bool, error) {
	if ancestor == "" || descendant == "" {
		return false, fmt.Errorf("ancestor and descendant must be provided")
	}
	_, err := common.ExecuteGitCommand(ctx, repoPath, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false, err
	}
	if exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func countCommitsSince(ctx context.Context, repoPath, base string) (int, error) {
	if strings.TrimSpace(base) == "" {
		return 0, fmt.Errorf("base hash is required to count commits")
	}
	out, err := common.ExecuteGitCommand(ctx, repoPath, "rev-list", "--count", fmt.Sprintf("%s..HEAD", base))
	if err != nil {
		return 0, fmt.Errorf("determine commits to rebase: %w", err)
	}
	countStr := strings.TrimSpace(string(out))
	if countStr == "" {
		return 0, nil
	}
	cnt, err := strconv.Atoi(countStr)
	if err != nil {
		return 0, fmt.Errorf("parse commit count: %w", err)
	}
	return cnt, nil
}

func rebaseOntoTarget(ctx context.Context, snapshot workspaceSnapshot, targetBase string, preserveAuthor *bool) error {
	keepAuthor := true
	if preserveAuthor != nil {
		keepAuthor = *preserveAuthor
	}

	args := []string{"rebase", "--reapply-cherry-picks", "--onto", targetBase, snapshot.BaseHash}

	env := append(os.Environ(), "GIT_SEQUENCE_EDITOR=true", "GIT_TERMINAL_PROMPT=0")

	if !keepAuthor {
		name, email := splitAuthor(snapshot.GitAuthor)
		if name != "" && email != "" {
			args = append([]string{"-c", fmt.Sprintf("user.name=%s", name), "-c", fmt.Sprintf("user.email=%s", email)}, args...)
		}
		args = append(args, "--exec", "git commit --amend --no-edit --reset-author")
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = snapshot.RepoPath
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = abortRebase(ctx, snapshot.RepoPath)
		return fmt.Errorf("git rebase failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func abortRebase(ctx context.Context, repoPath string) error {
	_, err := common.ExecuteGitCommand(ctx, repoPath, "rebase", "--abort")
	return err
}

func splitAuthor(author string) (string, string) {
	author = strings.TrimSpace(author)
	if author == "" {
		return "", ""
	}
	if !strings.Contains(author, "<") {
		return author, ""
	}
	idx := strings.Index(author, "<")
	name := strings.TrimSpace(author[:idx])
	email := strings.TrimSpace(strings.TrimSuffix(author[idx+1:], ">"))
	return name, email
}

func normalizeRemoteURL(url string) string {
	if url == "" {
		return ""
	}
	url = strings.TrimSpace(url)
	url = strings.TrimSuffix(url, "(fetch)")
	url = strings.TrimSuffix(url, "(push)")
	url = strings.TrimSpace(url)
	url = strings.TrimPrefix(url, "file://")
	if strings.HasPrefix(url, "ssh://") {
		url = strings.TrimPrefix(url, "ssh://")
	}
	cleaned := filepath.Clean(url)
	cleaned = strings.ReplaceAll(cleaned, "\\", "/")
	cleaned = strings.TrimRight(cleaned, "/")
	return cleaned
}

func hashesEqual(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if len(a) > 12 {
		a = a[:12]
	}
	if len(b) > 12 {
		b = b[:12]
	}
	return strings.EqualFold(a, b)
}

func shortHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}
