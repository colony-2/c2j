package squashrebasemerge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/gitprofile"
)

const defaultTargetBranch = "refs/heads/main"

// workspaceSnapshot captures relevant git context for the operation.
type workspaceSnapshot struct {
	RepoPath     string
	UpstreamRepo string
}

// Run executes the same primitives synchronously for Go callers. Durable
// checkpoints and retry guarantees are provided by GetOp's worker task chain.
func Run(ctx context.Context, input SquashRebaseMergeInput) (*SquashRebaseMergeOutput, error) {
	return run(ctx, input, false)
}

func run(ctx context.Context, input SquashRebaseMergeInput, emptyBase bool) (output *SquashRebaseMergeOutput, err error) {
	if err := requireCleanWorkspace(ctx, input.RepoPath); err != nil {
		return nil, err
	}
	original, err := common.GetCommitHash(ctx, input.RepoPath, "HEAD")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_, _ = common.ExecuteGitCommand(context.Background(), input.RepoPath, "reset", "--hard", original)
		}
	}()
	prepared, err := preparePublicationFrom(ctx, nil, input, emptyBase)
	if err != nil {
		return nil, err
	}
	rebased, err := rebasePublication(nil, ctx, prepared)
	if err != nil {
		return nil, err
	}
	result, err := mergePublication(nil, ctx, rebased)
	if err != nil {
		return nil, err
	}
	return &result, nil
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
	revision := persistHash
	if baseHash != "" {
		revision = baseHash + ".." + persistHash
	}
	out, err := common.ExecuteGitCommand(ctx, repoPath, "log", "--reverse", "--pretty=format:%an <%ae>%x1f%s%x1e", revision)
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

// commitTree preserves the selected tree without changing the workspace on failure.
func commitTree(ctx context.Context, repoPath, head, parent, author, message string) (string, error) {
	tree, err := common.GetCommitHash(ctx, repoPath, head+"^{tree}")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree, "-m", message}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	cmd := gitprofile.Command(ctx, args...)
	cmd.Dir = repoPath
	cmd.Env = gitprofile.Env(ctx, os.Environ())
	if author != "" {
		name, email, ok := strings.Cut(author, " <")
		if !ok || !strings.HasSuffix(email, ">") {
			return "", fmt.Errorf("invalid author %q", author)
		}
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+strings.TrimSuffix(email, ">"))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("create squash commit: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}
