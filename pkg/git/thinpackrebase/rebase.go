package thinpackrebase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/pkg/git/common"
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
	return run(ctx, input, "")
}

func run(ctx context.Context, input ThinpackRebaseInput, minimumBase string) (out *ThinpackRebaseOutput, runErr error) {
	if input.Resolution != nil {
		return nil, fmt.Errorf("resolved-tree adoption requires managed operation dependencies")
	}
	snapshot := workspaceSnapshot{RepoPath: input.RepoPath, BaseHash: input.BaseHash, PersistHash: input.PersistHash}
	phase := "prepare"
	clean, mutated := false, false
	defer func() {
		if runErr != nil {
			runErr = captureFailure(ctx, runErr, snapshot, input.TargetBaseHash, phase, clean, mutated)
		}
	}()
	targetBase := strings.TrimSpace(input.TargetBaseHash)
	if targetBase == "" {
		return nil, fmt.Errorf("target_base_hash is required")
	}

	var err error
	snapshot, err = resolveWorkspaceSnapshot(input)
	if err != nil {
		return nil, err
	}

	if err := common.ValidateRepositoryContext(ctx, snapshot.RepoPath); err != nil {
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

	phase = "fetch"
	if err := ensureTargetAvailable(ctx, snapshot.RepoPath, remoteName, targetBase); err != nil {
		return nil, err
	}

	phase = "ancestry"
	if minimumBase != "" {
		if err := ensureLinearHistory(ctx, snapshot.RepoPath, remoteName, minimumBase, targetBase); err != nil {
			return nil, fmt.Errorf("workspace base constraint: %w", err)
		}
	}

	if err := ensureLinearHistory(ctx, snapshot.RepoPath, remoteName, snapshot.BaseHash, targetBase); err != nil {
		return nil, err
	}

	phase = "prepare"
	// Resolve abbreviations before comparing or publishing metadata.
	targetBase, err = common.GetCommitHash(ctx, snapshot.RepoPath, targetBase+"^{commit}")
	if err != nil {
		return nil, err
	}
	input.TargetBaseHash = targetBase
	snapshot.BaseHash, err = common.GetCommitHash(ctx, snapshot.RepoPath, snapshot.BaseHash+"^{commit}")
	if err != nil {
		return nil, err
	}
	head, err := common.GetCommitHash(ctx, snapshot.RepoPath, "HEAD")
	if err != nil {
		return nil, err
	}
	requestedHead, err := common.GetCommitHash(ctx, snapshot.RepoPath, snapshot.PersistHash+"^{commit}")
	if err != nil {
		return nil, err
	}
	if requestedHead != head {
		return nil, fmt.Errorf("persist_hash does not match workspace HEAD")
	}
	snapshot.PersistHash = head
	if err := requireCleanIntegration(ctx, snapshot.RepoPath); err != nil {
		return nil, err
	}
	clean = true

	rebasedFrom := RebasedFromSummary{BaseHash: snapshot.BaseHash, PersistHash: snapshot.PersistHash}

	var newHead string
	var updatedRef string

	phase = "ancestry"
	targetInCandidate, err := isAncestor(ctx, snapshot.RepoPath, targetBase, head)
	if err != nil {
		return nil, err
	}

	// The recorded base can predate upstream history already incorporated by
	// this candidate. Exclude everything reachable from the pinned target,
	// including shared history brought in by a merge, rather than replaying it.
	commitCount, err := countCommitsSince(ctx, snapshot.RepoPath, targetBase)
	if err != nil {
		return nil, err
	}
	if !targetInCandidate && commitCount > 0 {
		phase = "ancestry"
		if err := ensureHistory(ctx, snapshot.RepoPath, remoteName, snapshot.BaseHash, targetBase, true); err != nil {
			return nil, err
		}
	}

	// Target deepening can also reveal the candidate's path to the recorded
	// base. Prove it before acceptance, without first fetching an unpublished
	// candidate commit from the upstream remote. A path through one merge parent
	// still does not establish completeness of the others.
	if err := ensureLinearHistory(ctx, snapshot.RepoPath, remoteName, snapshot.BaseHash, head); err != nil {
		return nil, fmt.Errorf("candidate ancestry: %w", err)
	}

	// Either retrieval can reveal incorporated history. Make the final selection
	// from the resulting graph, including candidate-side ancestry retrieval.
	targetInCandidate, err = isAncestor(ctx, snapshot.RepoPath, targetBase, head)
	if err != nil {
		return nil, err
	}
	commitCount, err = countCommitsSince(ctx, snapshot.RepoPath, targetBase)
	if err != nil {
		return nil, err
	}

	// An incorporated target adopts the base without rewriting any commit,
	// even when author reset was requested.
	if targetInCandidate {
		// Preserve the exact candidate HEAD and tree.
	} else if commitCount == 0 {
		// Nothing to reapply; align HEAD with the target base if needed.
		if !hashesEqual(snapshot.PersistHash, targetBase) {
			phase, mutated = "fast_forward", true
			if _, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "reset", "--hard", targetBase); err != nil {
				return nil, fmt.Errorf("fast-forward to target base %s: %w", shortHash(targetBase), err)
			}
		}
	} else {
		if err := requireReplayHistory(ctx, snapshot.RepoPath, head, targetBase); err != nil {
			return nil, err
		}
		phase, mutated = "replay", true
		if err := rebaseOntoTarget(ctx, snapshot, targetBase, input.PreserveAuthor); err != nil {
			return nil, err
		}
	}

	newHead, err = common.GetCommitHash(ctx, snapshot.RepoPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("determine rebased head: %w", err)
	}

	if strings.TrimSpace(input.UpdateRefs) != "" {
		phase = "update_ref"
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

// A shallow root cuts all paths through its parents. Unless the other revision
// also reaches that root, hidden history could change target..candidate (or hide
// target ancestry entirely). Reject before replay can discard merge resolutions.
// Shared roots are safe: their hidden history is excluded on both sides. Proven
// ancestral adoption and fast-forward do not replay and need no such restriction.
func requireReplayHistory(ctx context.Context, repoPath, candidate, target string) error {
	for _, pair := range [][2]string{{candidate, target}, {target, candidate}} {
		boundary, err := reachableShallowBoundary(ctx, repoPath, pair[0])
		if err != nil {
			return err
		}
		for _, root := range strings.Fields(boundary) {
			shared, err := isAncestor(ctx, repoPath, root, pair[1])
			if err != nil {
				return err
			}
			if !shared {
				return fmt.Errorf("cannot select replay: incomplete shallow history for %s", shortHash(pair[0]))
			}
		}
	}
	return nil
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

	if _, err := common.ExecuteGitCommand(ctx, repoPath, "fetch", "--depth", "1", remoteName, target); err != nil {
		return fmt.Errorf("fetch remote %s: %w", remoteName, err)
	}

	if common.CommitExists(ctx, repoPath, target) {
		return nil
	}
	return fmt.Errorf("target base hash %s not available after fetching %s", shortHash(target), remoteName)
}

// A present commit may still sit at a shallow boundary. Fetch the target's
// history incrementally until ancestry is proved or its reachable history is
// complete. Never unshallow the whole repository just to refresh a workspace.
func ensureLinearHistory(ctx context.Context, repoPath, remoteName, currentBase, targetBase string) error {
	return ensureHistory(ctx, repoPath, remoteName, currentBase, targetBase, false)
}

func ensureHistory(ctx context.Context, repoPath, remoteName, currentBase, targetBase string, completeReplayRange bool) error {
	if hashesEqual(currentBase, targetBase) {
		return nil
	}
	depth := 2
	previousBoundary := ""
	for {
		ancestor, err := isAncestor(ctx, repoPath, currentBase, targetBase)
		if err != nil {
			return err
		}
		if ancestor && !completeReplayRange {
			return nil
		}
		boundary, err := reachableShallowBoundary(ctx, repoPath, targetBase)
		if err != nil {
			return err
		}
		if ancestor {
			if boundary == "" {
				return nil
			}
			// One merge parent can prove base ancestry while a shallow second
			// parent still hides commits shared with HEAD. Boundaries already
			// excluded by the base or shared with HEAD cannot change replay.
			pending, err := common.ExecuteGitCommand(ctx, repoPath, "rev-list", targetBase, "^"+currentBase)
			if err != nil {
				return err
			}
			rangeCommits := map[string]bool{}
			for _, hash := range strings.Fields(string(pending)) {
				rangeCommits[hash] = true
			}
			var relevant []string
			for _, hash := range strings.Fields(boundary) {
				if rangeCommits[hash] {
					shared, err := isAncestor(ctx, repoPath, hash, "HEAD")
					if err != nil {
						return err
					}
					if !shared {
						relevant = append(relevant, hash)
					}
				}
			}
			boundary = strings.Join(relevant, "\n")
			if boundary == "" {
				return nil
			}
		}
		if boundary == "" {
			return fmt.Errorf("target base %s does not descend from current base %s", shortHash(targetBase), shortHash(currentBase))
		}
		if remoteName == "" || boundary == previousBoundary || depth > 1048576 {
			return fmt.Errorf("cannot verify target ancestry: incomplete shallow history for %s", shortHash(targetBase))
		}
		previousBoundary = boundary
		// Start beyond the already available history; don't make it shallower.
		count, err := common.ExecuteGitCommand(ctx, repoPath, "rev-list", "--count", targetBase)
		if err != nil {
			return err
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(count)))
		if err != nil {
			return err
		}
		if depth <= n {
			depth = n + 1
		}
		if _, err := common.ExecuteGitCommand(ctx, repoPath, "fetch", "--depth", strconv.Itoa(depth), remoteName, targetBase); err != nil {
			return fmt.Errorf("fetch target ancestry: %w", err)
		}
		depth *= 2
	}
}

func reachableShallowBoundary(ctx context.Context, repoPath, target string) (string, error) {
	path, err := common.ExecuteGitCommand(ctx, repoPath, "rev-parse", "--git-path", "shallow")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(path))
	if !filepath.IsAbs(name) {
		name = filepath.Join(repoPath, name)
	}
	shallow, err := os.ReadFile(name)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	roots, err := common.ExecuteGitCommand(ctx, repoPath, "rev-list", "--max-parents=0", target)
	if err != nil {
		return "", err
	}
	boundaries := map[string]bool{}
	for _, hash := range strings.Fields(string(shallow)) {
		boundaries[hash] = true
	}
	var reachable []string
	for _, hash := range strings.Fields(string(roots)) {
		if boundaries[hash] {
			reachable = append(reachable, hash)
		}
	}
	return strings.Join(reachable, "\n"), nil
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
	if err := common.EnsureGitIdentity(ctx, snapshot.RepoPath); err != nil {
		return err
	}
	keepAuthor := true
	if preserveAuthor != nil {
		keepAuthor = *preserveAuthor
	}

	args := []string{"rebase", "--no-fork-point", "--reapply-cherry-picks", "--onto", targetBase, targetBase}

	env := append(os.Environ(), "GIT_SEQUENCE_EDITOR=true", "GIT_TERMINAL_PROMPT=0")

	// Rebase always creates commits, even when retaining their original author.
	// Fresh worker clones do not inherit the source repository's Git config.
	name, email := splitAuthor(snapshot.GitAuthor)
	if name != "" && email != "" {
		args = append([]string{"-c", fmt.Sprintf("user.name=%s", name), "-c", fmt.Sprintf("user.email=%s", email)}, args...)
	}
	if !keepAuthor {
		args = append(args, "--exec", "git commit --amend --no-edit --reset-author")
	}

	cmd := gitprofile.Command(ctx, args...)
	cmd.Dir = snapshot.RepoPath
	cmd.Env = gitprofile.Env(ctx, env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := gitprofile.Run(ctx, cmd)
	if err != nil {
		output := append(stdout.Bytes(), stderr.Bytes()...)
		return &replayError{cause: errors.Join(err, context.Cause(ctx)), output: output, stderr: stderr.Bytes()}
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
	return strings.EqualFold(a, b)
}

func shortHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}
