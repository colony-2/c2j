package squashrebasemerge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/git/pkg/common"
)

const defaultTargetBranch = "refs/heads/main"

// workspaceSnapshot captures relevant git context for the operation.
type workspaceSnapshot struct {
	RepoPath     string
	BaseHash     string
	PersistHash  string
	UpstreamRepo string
}

// Run executes the squash-rebase-merge flow.
func Run(ctx context.Context, input SquashRebaseMergeInput) (*SquashRebaseMergeOutput, error) {
	snapshot, err := resolveWorkspaceSnapshot(input)
	if err != nil {
		return nil, err
	}

	if err := common.ValidateRepository(snapshot.RepoPath); err != nil {
		return nil, fmt.Errorf("validate repository: %w", err)
	}

	targetBranch := strings.TrimSpace(input.UpstreamBranch)
	if targetBranch == "" {
		targetBranch = defaultTargetBranch
	}

	if snapshot.PersistHash == "" {
		hash, err := common.GetCommitHash(ctx, snapshot.RepoPath, "HEAD")
		if err != nil {
			return nil, fmt.Errorf("determine current HEAD: %w", err)
		}
		snapshot.PersistHash = hash
	}

	remoteName, err := determineRemoteName(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(remoteName) == "" {
		return nil, fmt.Errorf("no remote configured for squashrebasemerge")
	}

	if err := fetchTargetBranch(ctx, snapshot.RepoPath, remoteName, targetBranch); err != nil {
		return nil, err
	}

	remoteRef := remoteTrackingRef(remoteName, targetBranch)
	remoteTip, err := common.GetCommitHash(ctx, snapshot.RepoPath, remoteRef)
	if err != nil {
		return nil, fmt.Errorf("resolve remote target %s: %w", remoteRef, err)
	}

	baseHash, err := mergeBase(ctx, snapshot.RepoPath, snapshot.PersistHash, remoteTip)
	if err != nil {
		return nil, err
	}
	if baseHash == "" {
		return nil, fmt.Errorf("unable to determine merge base between local %s and %s", shortHash(snapshot.PersistHash), shortHash(remoteTip))
	}
	snapshot.BaseHash = baseHash

	doRebase := true
	if input.Rebase != nil {
		doRebase = *input.Rebase
	}
	rangeBaseHash := snapshot.BaseHash
	if !doRebase {
		fastForwardable, err := isAncestor(ctx, snapshot.RepoPath, remoteTip, snapshot.PersistHash)
		if err != nil {
			return nil, fmt.Errorf("check fast-forward eligibility: %w", err)
		}
		if !fastForwardable {
			return nil, common.NotFastForwardError{
				TargetBranch: targetBranch,
				LocalHead:    snapshot.PersistHash,
				UpstreamHead: remoteTip,
			}
		}
		rangeBaseHash = remoteTip
	}

	newlyCommitted, err := countCommitsBetween(ctx, snapshot.RepoPath, rangeBaseHash, snapshot.PersistHash)
	if err != nil {
		return nil, err
	}

	if newlyCommitted == 0 {
		// Nothing to merge; fast-forward local context to remote tip.
		if _, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "reset", "--hard", remoteTip); err != nil {
			return nil, fmt.Errorf("fast-forward workspace to %s: %w", shortHash(remoteTip), err)
		}
		return &SquashRebaseMergeOutput{
			TargetBranch: targetBranch,
			RemoteRef:    remoteRef,
			MergedHash:   remoteTip,
			SquashedCommits: SquashRangeSummary{
				BaseHash:    snapshot.BaseHash,
				PersistHash: snapshot.PersistHash,
			},
			GitContextPatch: map[string]interface{}{
				"base_hash":     remoteTip,
				"persist_hash":  remoteTip,
				"previous_hash": snapshot.PersistHash,
			},
			FastForward: !doRebase,
		}, nil
	}

	commitDetails, err := collectCommitDetails(ctx, snapshot.RepoPath, rangeBaseHash, snapshot.PersistHash)
	if err != nil {
		return nil, err
	}

	originalHead := snapshot.PersistHash
	restoreOnError := func() {
		_, _ = common.ExecuteGitCommand(context.Background(), snapshot.RepoPath, "reset", "--hard", originalHead)
	}

	commitAuthor, commitMessage := buildSquashCommit(input, commitDetails)
	if err := createSquashCommit(ctx, snapshot, rangeBaseHash, commitMessage, commitAuthor); err != nil {
		restoreOnError()
		return nil, err
	}

	var mergedHash string
	if !doRebase {
		mergedHash, err = common.GetCommitHash(ctx, snapshot.RepoPath, "HEAD")
		if err != nil {
			restoreOnError()
			return nil, fmt.Errorf("determine merged head: %w", err)
		}

		stillAncestor, err := isAncestor(ctx, snapshot.RepoPath, remoteTip, mergedHash)
		if err != nil {
			restoreOnError()
			return nil, fmt.Errorf("confirm fast-forward ancestor: %w", err)
		}
		if !stillAncestor {
			restoreOnError()
			return nil, common.NotFastForwardError{
				TargetBranch: targetBranch,
				LocalHead:    mergedHash,
				UpstreamHead: remoteTip,
			}
		}

		if _, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "push", remoteName, fmt.Sprintf("HEAD:%s", targetBranch)); err != nil {
			restoreOnError()
			if isNonFastForwardError(err) {
				return nil, common.NotFastForwardError{
					TargetBranch: targetBranch,
					LocalHead:    mergedHash,
					UpstreamHead: remoteTip,
				}
			}
			return nil, fmt.Errorf("push to %s: %w", targetBranch, err)
		}
	} else {
		if err := rebaseOntoTarget(ctx, snapshot.RepoPath, remoteTip, snapshot.BaseHash); err != nil {
			restoreOnError()
			return nil, err
		}

		mergedHash, err = common.GetCommitHash(ctx, snapshot.RepoPath, "HEAD")
		if err != nil {
			restoreOnError()
			return nil, fmt.Errorf("determine merged head: %w", err)
		}

		if _, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "push", remoteName, fmt.Sprintf("HEAD:%s", targetBranch)); err != nil {
			restoreOnError()
			return nil, fmt.Errorf("push to %s: %w", targetBranch, err)
		}
	}

	return &SquashRebaseMergeOutput{
		TargetBranch: targetBranch,
		RemoteRef:    remoteRef,
		MergedHash:   mergedHash,
		SquashedCommits: SquashRangeSummary{
			BaseHash:    snapshot.BaseHash,
			PersistHash: snapshot.PersistHash,
		},
		GitContextPatch: map[string]interface{}{
			"base_hash":     mergedHash,
			"persist_hash":  mergedHash,
			"previous_hash": snapshot.PersistHash,
		},
		FastForward: !doRebase,
	}, nil
}

func resolveWorkspaceSnapshot(input SquashRebaseMergeInput) (workspaceSnapshot, error) {
	snapshot := workspaceSnapshot{}
	repoPath := strings.TrimSpace(input.RepoPath)
	if repoPath == "" {
		return snapshot, fmt.Errorf("repo_path is required")
	}

	snapshot.RepoPath = repoPath
	snapshot.PersistHash = strings.TrimSpace(input.LocalHash)
	snapshot.UpstreamRepo = strings.TrimSpace(input.UpstreamRepo)

	return snapshot, nil
}

func determineRemoteName(ctx context.Context, snapshot workspaceSnapshot) (string, error) {
	if strings.TrimSpace(snapshot.UpstreamRepo) == "" {
		return "", fmt.Errorf("upstream_repo is required")
	}

	out, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "remote", "-v")
	if err != nil {
		return "", fmt.Errorf("list remotes: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	baseNorm := normalizeRemoteURL(snapshot.UpstreamRepo)
	remoteSeen := make(map[string]struct{})
	var first string

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
		if first == "" {
			first = name
		}
		if baseNorm != "" && normalizeRemoteURL(url) == baseNorm {
			return name, nil
		}
	}

	if len(remoteSeen) == 1 {
		return first, nil
	}

	if len(remoteSeen) == 0 {
		return "", fmt.Errorf("repository has no remotes configured")
	}

	return "", fmt.Errorf("unable to determine remote for upstream_repo %s", snapshot.UpstreamRepo)
}

func fetchTargetBranch(ctx context.Context, repoPath, remoteName, targetBranch string) error {
	if _, err := common.ExecuteGitCommand(ctx, repoPath, "fetch", remoteName, targetBranch); err != nil {
		return fmt.Errorf("fetch %s %s: %w", remoteName, targetBranch, err)
	}
	return nil
}

func remoteTrackingRef(remoteName, targetBranch string) string {
	short := strings.TrimPrefix(targetBranch, "refs/")
	short = strings.TrimPrefix(short, "heads/")
	if short == targetBranch {
		short = strings.TrimPrefix(targetBranch, "refs/heads/")
	}
	short = strings.TrimPrefix(short, "heads/")
	short = strings.TrimPrefix(short, "remotes/")
	return fmt.Sprintf("%s/%s", remoteName, short)
}

type commitDetail struct {
	Author  string
	Subject string
}

func collectCommitDetails(ctx context.Context, repoPath, baseHash, persistHash string) ([]commitDetail, error) {
	out, err := common.ExecuteGitCommand(ctx, repoPath, "log", "--reverse", "--pretty=format:%an <%ae>%x1f%s%x1e", fmt.Sprintf("%s..%s", baseHash, persistHash))
	if err != nil {
		return nil, fmt.Errorf("collect commit summaries: %w", err)
	}
	raw := strings.TrimRight(string(out), "\x1e")
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	records := strings.Split(raw, "\x1e")
	details := make([]commitDetail, 0, len(records))
	for _, record := range records {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\x1f", 2)
		if len(parts) != 2 {
			continue
		}
		author := strings.TrimSpace(parts[0])
		subject := strings.TrimSpace(parts[1])
		details = append(details, commitDetail{
			Author:  author,
			Subject: subject,
		})
	}
	return details, nil
}

func buildSquashCommit(input SquashRebaseMergeInput, details []commitDetail) (string, string) {
	message := strings.TrimSpace(input.CommitMessage)
	if message == "" {
		subjects := make([]string, 0, len(details))
		for _, detail := range details {
			if detail.Subject == "" {
				continue
			}
			subjects = append(subjects, detail.Subject)
		}
		message = strings.TrimSpace(strings.Join(subjects, "\n"))
	}
	if message == "" {
		message = "Squash commit"
	}

	author := strings.TrimSpace(input.Author)
	if author == "" && len(details) > 0 {
		author = strings.TrimSpace(details[0].Author)
	}

	coauthors := uniqueAuthors(details, author)
	message = appendCoauthors(message, coauthors)
	return author, message
}

func uniqueAuthors(details []commitDetail, primary string) []string {
	seen := make(map[string]struct{})
	primary = strings.TrimSpace(primary)
	if primary != "" {
		seen[strings.ToLower(primary)] = struct{}{}
	}

	authors := make([]string, 0, len(details))
	for _, detail := range details {
		author := strings.TrimSpace(detail.Author)
		if author == "" {
			continue
		}
		key := strings.ToLower(author)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		authors = append(authors, author)
	}
	return authors
}

func appendCoauthors(message string, coauthors []string) string {
	message = strings.TrimRight(message, "\n")
	if len(coauthors) == 0 {
		return message
	}
	var b strings.Builder
	if message != "" {
		b.WriteString(message)
		b.WriteString("\n\n")
	}
	for _, author := range coauthors {
		fmt.Fprintf(&b, "Co-authored-by: %s\n", author)
	}
	return strings.TrimRight(b.String(), "\n")
}

func createSquashCommit(ctx context.Context, snapshot workspaceSnapshot, squashBaseHash, commitMessage, author string) error {
	if _, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "reset", "--soft", squashBaseHash); err != nil {
		return fmt.Errorf("prepare squash reset: %w", err)
	}

	args := []string{"commit", "-m", commitMessage}
	env := os.Environ()
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	if author != "" {
		args = append(args, "--author", author)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = snapshot.RepoPath
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("create squash commit: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

func rebaseOntoTarget(ctx context.Context, repoPath, remoteTip, originalBase string) error {
	args := []string{"rebase", "--reapply-cherry-picks", "--onto", remoteTip, originalBase}

	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SEQUENCE_EDITOR=true")

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = abortRebase(ctx, repoPath)
		return fmt.Errorf("git rebase failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func abortRebase(ctx context.Context, repoPath string) error {
	_, err := common.ExecuteGitCommand(ctx, repoPath, "rebase", "--abort")
	return err
}

func countCommitsBetween(ctx context.Context, repoPath, baseHash, persistHash string) (int, error) {
	if strings.TrimSpace(baseHash) == "" || strings.TrimSpace(persistHash) == "" {
		return 0, fmt.Errorf("base and persist hashes are required to compute squash range")
	}
	out, err := common.ExecuteGitCommand(ctx, repoPath, "rev-list", "--count", fmt.Sprintf("%s..%s", baseHash, persistHash))
	if err != nil {
		return 0, fmt.Errorf("count commits between %s and %s: %w", shortHash(baseHash), shortHash(persistHash), err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return 0, nil
	}
	count, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("parse commit count: %w", err)
	}
	return count, nil
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

func mergeBase(ctx context.Context, repoPath, left, right string) (string, error) {
	out, err := common.ExecuteGitCommand(ctx, repoPath, "merge-base", left, right)
	if err != nil {
		return "", fmt.Errorf("determine merge-base for %s and %s: %w", shortHash(left), shortHash(right), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func normalizeRemoteURL(url string) string {
	if url == "" {
		return ""
	}
	cleaned := strings.TrimSpace(url)
	cleaned = strings.TrimSuffix(cleaned, "(fetch)")
	cleaned = strings.TrimSuffix(cleaned, "(push)")
	cleaned = strings.TrimSpace(cleaned)
	cleaned = strings.TrimPrefix(cleaned, "file://")
	if strings.HasPrefix(cleaned, "ssh://") {
		cleaned = strings.TrimPrefix(cleaned, "ssh://")
	}
	cleaned = strings.ReplaceAll(cleaned, "\\", "/")
	cleaned = strings.TrimRight(cleaned, "/")
	return cleaned
}

func shortHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

func isNonFastForwardError(err error) bool {
	if err == nil {
		return false
	}
	lowered := strings.ToLower(err.Error())
	return strings.Contains(lowered, "non-fast-forward") || strings.Contains(lowered, "[rejected]")
}
