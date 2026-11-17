package thinpackrebase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/ops"
)

func adopt(ctx context.Context, inv ops.OpDependencies, input ThinpackRebaseInput, minimumBase string) (out *ThinpackRebaseOutput, runErr error) {
	snapshot := workspaceSnapshot{RepoPath: input.RepoPath, BaseHash: input.BaseHash, PersistHash: input.PersistHash}
	phase, clean, mutated := "validate_resolution", false, false
	ref, oldRef := strings.TrimSpace(input.UpdateRefs), ""
	refChanged := false
	defer func() {
		if runErr == nil {
			return
		}
		runErr = captureFailure(ctx, runErr, snapshot, input.TargetBaseHash, phase, clean, mutated)
		if refChanged {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			args := []string{"update-ref", ref, oldRef}
			if oldRef == "" {
				args = []string{"update-ref", "-d", ref}
			}
			if _, err := common.ExecuteGitCommand(cleanup, snapshot.RepoPath, args...); err != nil {
				var failure *Failure
				if errors.As(runErr, &failure) {
					failure.Details.WorkspaceRestored = false
					failure.Details.CleanupError += " restore ref: " + err.Error()
				}
			}
		}
	}()
	if err := input.ValidateOpInput(); err != nil {
		return nil, err
	}
	if inv == nil || input.RepoPath != inv.WorktreePath() {
		return nil, fmt.Errorf("resolved-tree adoption requires the active managed workspace")
	}
	if _, ok := inv.(interface{ SetWorkspaceBase(string) }); !ok {
		return nil, fmt.Errorf("workspace base updates are unavailable")
	}
	g := inv.GitContext()
	if g.Const {
		return nil, fmt.Errorf("resolved-tree adoption requires workspace persistence; const operations discard changes")
	}
	if g.ResolvedBaseHash == "" || !hashesEqual(input.BaseHash, g.ResolvedBaseHash) {
		return nil, fmt.Errorf("base_hash does not match authoritative managed base")
	}
	accepted := g.PersistHash
	if accepted == "" {
		accepted = g.ResolvedBaseHash
	}
	if !hashesEqual(input.PersistHash, accepted) {
		return nil, fmt.Errorf("persist_hash does not match accepted managed candidate")
	}
	var err error
	snapshot, err = resolveWorkspaceSnapshot(input)
	if err != nil {
		return nil, err
	}
	// Canonicalize only after validating full literal object IDs; never peel tags.
	snapshot.BaseHash = strings.ToLower(snapshot.BaseHash)
	snapshot.PersistHash = strings.ToLower(snapshot.PersistHash)
	target, tree := strings.ToLower(input.TargetBaseHash), strings.ToLower(input.Resolution.TreeHash)
	if err := common.ValidateRepositoryContext(ctx, snapshot.RepoPath); err != nil {
		return nil, err
	}
	if err := checkConfirmedTree(ctx, snapshot, tree); err != nil {
		return nil, err
	}
	clean = true
	remote, err := determineRemoteName(ctx, snapshot, input.UpstreamRemote)
	if err != nil {
		return nil, err
	}
	phase = "fetch"
	if err := ensureTargetAvailable(ctx, snapshot.RepoPath, remote, target); err != nil {
		return nil, err
	}
	for _, object := range []struct{ hash, kind string }{
		{snapshot.BaseHash, "commit"}, {snapshot.PersistHash, "commit"}, {target, "commit"}, {tree, "tree"},
	} {
		kind, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "cat-file", "-t", object.hash)
		if err != nil {
			return nil, fmt.Errorf("read confirmed %s %s: %w", object.kind, object.hash, err)
		}
		if strings.TrimSpace(string(kind)) != object.kind {
			return nil, fmt.Errorf("confirmed object %s must be a %s", object.hash, object.kind)
		}
	}
	phase = "ancestry"
	for _, relation := range [][2]string{{snapshot.BaseHash, snapshot.PersistHash}, {snapshot.BaseHash, target}, {minimumBase, target}} {
		if relation[0] == "" {
			continue
		}
		if err := ensureLinearHistory(ctx, snapshot.RepoPath, remote, relation[0], relation[1]); err != nil {
			return nil, fmt.Errorf("workspace ancestry constraint: %w", err)
		}
	}
	if ref != "" {
		if _, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "check-ref-format", ref); err != nil {
			return nil, fmt.Errorf("invalid update_refs: %w", err)
		}
		exists, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "rev-parse", "--verify", "--quiet", ref)
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				return nil, err
			}
		} else {
			oldRef = strings.TrimSpace(string(exists))
		}
	}
	phase = "construct_resolution"
	targetTree, err := common.GetCommitHash(ctx, snapshot.RepoPath, target+"^{tree}")
	if err != nil {
		return nil, err
	}
	newHead := target
	if tree != targetTree {
		newHead, err = constructResolution(ctx, snapshot, target, tree, input.PreserveAuthor)
		if err != nil {
			return nil, err
		}
		actual, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "show", "-s", "--format=%T %P", newHead)
		if err != nil {
			return nil, fmt.Errorf("verify constructed candidate: %w", err)
		}
		if strings.TrimSpace(string(actual)) != tree+" "+target {
			return nil, fmt.Errorf("constructed candidate tree or sole parent differs: %s", actual)
		}
	}
	// Object retrieval/construction may take time. Recheck before installing.
	if err := checkConfirmedTree(ctx, snapshot, tree); err != nil {
		return nil, err
	}
	phase, mutated = "install_resolution", true
	if _, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "reset", "--hard", newHead); err != nil {
		return nil, fmt.Errorf("install resolved candidate: %w", err)
	}
	if err := checkConfirmedTree(ctx, workspaceSnapshot{RepoPath: snapshot.RepoPath, PersistHash: newHead}, tree); err != nil {
		return nil, err
	}
	if ref != "" {
		phase = "update_ref"
		if _, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "update-ref", ref, newHead); err != nil {
			return nil, fmt.Errorf("update ref %s: %w", ref, err)
		}
		refChanged = true
		actualRef, err := common.GetCommitHash(ctx, snapshot.RepoPath, ref)
		if err != nil {
			return nil, fmt.Errorf("verify updated ref %s: %w", ref, err)
		}
		if actualRef != newHead {
			return nil, fmt.Errorf("updated ref %s differs from candidate", ref)
		}
	}
	return &ThinpackRebaseOutput{
		TargetBaseHash: target, NewBaseHash: target, NewPersistHash: newHead, UpdatedRef: ref,
		RebasedFrom:     RebasedFromSummary{BaseHash: snapshot.BaseHash, PersistHash: snapshot.PersistHash},
		GitContextPatch: map[string]any{"base_hash": target, "persist_hash": newHead, "previous_hash": snapshot.PersistHash},
	}, nil
}

func checkConfirmedTree(ctx context.Context, snapshot workspaceSnapshot, tree string) error {
	head, err := common.GetCommitHash(ctx, snapshot.RepoPath, "HEAD")
	if err != nil {
		return err
	}
	if head != snapshot.PersistHash {
		return fmt.Errorf("persist_hash does not match workspace HEAD")
	}
	actual, err := common.GetCommitHash(ctx, snapshot.RepoPath, "HEAD^{tree}")
	if err != nil {
		return err
	}
	if actual != tree {
		return fmt.Errorf("resolution.tree_hash does not match candidate tree")
	}
	return requireCleanIntegration(ctx, snapshot.RepoPath)
}

func requireCleanIntegration(ctx context.Context, repo string) error {
	for _, state := range []string{"MERGE_HEAD", "rebase-apply", "rebase-merge", "CHERRY_PICK_HEAD", "REVERT_HEAD", "sequencer"} {
		path, err := common.ExecuteGitCommand(ctx, repo, "rev-parse", "--git-path", state)
		if err != nil {
			return err
		}
		name := strings.TrimSpace(string(path))
		if !filepath.IsAbs(name) {
			name = filepath.Join(repo, name)
		}
		if _, err := os.Lstat(name); err == nil {
			return fmt.Errorf("unfinished Git integration state: %s", state)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	status, err := common.ExecuteGitCommand(ctx, repo, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return fmt.Errorf("Git operation requires a clean workspace and index; commit pending changes first")
	}
	return nil
}

func constructResolution(ctx context.Context, snapshot workspaceSnapshot, target, tree string, preserve *bool) (string, error) {
	// Read raw commit bytes so the full message, including trailing newlines, is
	// carried through stdin rather than rewritten by pretty-format or a shell.
	raw, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "cat-file", "commit", snapshot.PersistHash)
	if err != nil {
		return "", err
	}
	headers, message, ok := bytes.Cut(raw, []byte("\n\n"))
	if !ok {
		return "", fmt.Errorf("candidate has no commit message separator")
	}
	var author string
	for _, line := range strings.Split(string(headers), "\n") {
		if strings.HasPrefix(line, "author ") {
			author = strings.TrimPrefix(line, "author ")
		}
	}
	end := strings.LastIndex(author, ">")
	if end < 0 {
		return "", fmt.Errorf("candidate author is invalid")
	}
	name, email := splitAuthor(author[:end+1])
	date := strings.TrimSpace(author[end+1:])
	if preserve != nil && !*preserve {
		name, email = splitAuthor(snapshot.GitAuthor)
		date = "" // configured identity, with a fresh author date
	}
	args := []string{"commit-tree", tree, "-p", target}
	committerName, committerEmail := splitAuthor(snapshot.GitAuthor)
	if committerName != "" && committerEmail != "" {
		args = append([]string{"-c", "user.name=" + committerName, "-c", "user.email=" + committerEmail}, args...)
	}
	cmd := gitprofile.Command(ctx, args...)
	cmd.Dir = snapshot.RepoPath
	env := os.Environ()
	if name != "" && email != "" {
		env = append(env, "GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email)
	}
	if date != "" {
		env = append(env, "GIT_AUTHOR_DATE="+date)
	}
	cmd.Env = gitprofile.Env(ctx, env)
	cmd.Stdin = bytes.NewReader(message)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := gitprofile.Run(ctx, cmd); err != nil {
		return "", fmt.Errorf("construct resolved candidate: %s: %w", stderr.String(), err)
	}
	hash := strings.TrimSpace(stdout.String())
	constructed, err := common.ExecuteGitCommand(ctx, snapshot.RepoPath, "cat-file", "commit", hash)
	if err != nil {
		return "", err
	}
	newHeaders, newMessage, ok := bytes.Cut(constructed, []byte("\n\n"))
	if !ok {
		return "", fmt.Errorf("constructed candidate has no message separator")
	}
	if bytes.Equal(message, newMessage) {
		return hash, nil
	}
	// commit-tree completes a missing final newline. Git also permits commits
	// without it; preserve those message bytes rather than normalizing them.
	cmd = gitprofile.Command(ctx, "hash-object", "-w", "-t", "commit", "--stdin")
	cmd.Dir = snapshot.RepoPath
	cmd.Stdin = bytes.NewReader(bytes.Join([][]byte{newHeaders, message}, []byte("\n\n")))
	stdout.Reset()
	stderr.Reset()
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := gitprofile.Run(ctx, cmd); err != nil {
		return "", fmt.Errorf("preserve resolved candidate message: %s: %w", stderr.String(), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}
