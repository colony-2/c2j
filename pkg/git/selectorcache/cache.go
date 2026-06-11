// Package selectorcache materializes git refs into reusable source directories.
package selectorcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	cacheVersion = 1
	metadataFile = ".c2j-source-cache.json"
)

type Cache struct {
	Root string

	mu       sync.Mutex
	inFlight map[string]*call
}

type ResolveRequest struct {
	RepositoryURL string
	Ref           string
}

type ResolveResult struct {
	RepositoryURL string
	RepoKey       string
	Ref           string
	Commit        string
	SourceDir     string
}

type sourceMetadata struct {
	Version       int       `json:"version"`
	RepositoryURL string    `json:"repository_url"`
	Commit        string    `json:"commit"`
	CreatedAt     time.Time `json:"created_at"`
}

type remoteResolution struct {
	commit   string
	fetchRef string
}

type call struct {
	done chan struct{}
	err  error
}

var defaultCache = &Cache{}

func Default() *Cache {
	return defaultCache
}

func (c *Cache) Resolve(ctx context.Context, req ResolveRequest) (ResolveResult, error) {
	repoURL := strings.TrimSpace(req.RepositoryURL)
	if repoURL == "" {
		return ResolveResult{}, fmt.Errorf("repository URL is required")
	}
	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		return ResolveResult{}, fmt.Errorf("git ref is required")
	}

	root, err := c.cacheRoot()
	if err != nil {
		return ResolveResult{}, err
	}
	repoKey := cacheRepoKey(repoURL)

	var commit, fetchRef string
	if isFullGitHash(ref) {
		commit = strings.ToLower(ref)
		fetchRef = commit
	} else {
		resolved, err := resolveRemoteRef(ctx, repoURL, ref)
		if err != nil {
			return ResolveResult{}, err
		}
		commit = resolved.commit
		fetchRef = resolved.fetchRef
	}

	sourceDir := commitSourceDir(root, repoKey, commit)
	if dirExists(sourceDir) {
		return ResolveResult{
			RepositoryURL: repoURL,
			RepoKey:       repoKey,
			Ref:           ref,
			Commit:        commit,
			SourceDir:     sourceDir,
		}, nil
	}

	key := repoURL + "\x00" + commit
	if err := c.do(key, func() error {
		if dirExists(sourceDir) {
			return nil
		}
		return materializeCommit(ctx, root, repoURL, repoKey, commit, fetchRef)
	}); err != nil {
		return ResolveResult{}, err
	}

	return ResolveResult{
		RepositoryURL: repoURL,
		RepoKey:       repoKey,
		Ref:           ref,
		Commit:        commit,
		SourceDir:     sourceDir,
	}, nil
}

func (c *Cache) cacheRoot() (string, error) {
	if root := strings.TrimSpace(c.Root); root != "" {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", fmt.Errorf("create git selector cache root: %w", err)
		}
		return root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for git selector cache: %w", err)
	}
	root := filepath.Join(home, ".c2j", "cache", "git-selectors", "v1")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create git selector cache root: %w", err)
	}
	return root, nil
}

func (c *Cache) do(key string, fn func() error) error {
	c.mu.Lock()
	if c.inFlight == nil {
		c.inFlight = map[string]*call{}
	}
	if existing := c.inFlight[key]; existing != nil {
		c.mu.Unlock()
		<-existing.done
		return existing.err
	}
	current := &call{done: make(chan struct{})}
	c.inFlight[key] = current
	c.mu.Unlock()

	current.err = fn()
	close(current.done)

	c.mu.Lock()
	delete(c.inFlight, key)
	c.mu.Unlock()

	return current.err
}

func resolveRemoteRef(ctx context.Context, repoURL string, ref string) (remoteResolution, error) {
	ref = strings.TrimSpace(ref)
	patterns := lsRemotePatterns(ref)
	out, err := runGitOutput(ctx, "", append([]string{"ls-remote", repoURL}, patterns...)...)
	if err != nil {
		return remoteResolution{}, fmt.Errorf("resolve git ref %q in %q: %w", ref, repoURL, err)
	}
	matches := parseLSRemote(out)
	if len(matches) == 0 {
		return remoteResolution{}, fmt.Errorf("resolve git ref %q in %q: ref not found", ref, repoURL)
	}
	match, ok := chooseLSRemoteMatch(ref, matches)
	if !ok {
		return remoteResolution{}, fmt.Errorf("resolve git ref %q in %q: ref not found", ref, repoURL)
	}
	commit := strings.ToLower(match.hash)
	if !isFullGitHash(commit) {
		return remoteResolution{}, fmt.Errorf("resolve git ref %q in %q: resolved object %q is not a commit hash", ref, repoURL, match.hash)
	}
	return remoteResolution{
		commit:   commit,
		fetchRef: fetchableRemoteRef(match.refName, ref),
	}, nil
}

func lsRemotePatterns(ref string) []string {
	if ref == "HEAD" {
		return []string{"HEAD"}
	}
	if strings.HasPrefix(ref, "refs/tags/") {
		return []string{ref, ref + "^{}"}
	}
	if strings.HasPrefix(ref, "refs/") {
		return []string{ref}
	}
	return []string{
		"refs/heads/" + ref,
		"refs/tags/" + ref,
		"refs/tags/" + ref + "^{}",
		ref,
	}
}

type lsRemoteMatch struct {
	hash    string
	refName string
}

func parseLSRemote(out string) []lsRemoteMatch {
	lines := strings.Split(out, "\n")
	matches := make([]lsRemoteMatch, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		matches = append(matches, lsRemoteMatch{
			hash:    fields[0],
			refName: fields[1],
		})
	}
	return matches
}

func chooseLSRemoteMatch(ref string, matches []lsRemoteMatch) (lsRemoteMatch, bool) {
	preferred := preferredRemoteRefs(ref)
	for _, want := range preferred {
		for _, match := range matches {
			if match.refName == want {
				return match, true
			}
		}
	}
	if len(matches) == 0 {
		return lsRemoteMatch{}, false
	}
	return matches[0], true
}

func preferredRemoteRefs(ref string) []string {
	if ref == "HEAD" {
		return []string{"HEAD"}
	}
	if strings.HasPrefix(ref, "refs/tags/") {
		return []string{ref + "^{}", ref}
	}
	if strings.HasPrefix(ref, "refs/") {
		return []string{ref}
	}
	return []string{
		"refs/heads/" + ref,
		"refs/tags/" + ref + "^{}",
		"refs/tags/" + ref,
		ref,
	}
}

func fetchableRemoteRef(remoteRef string, submittedRef string) string {
	remoteRef = strings.TrimSuffix(strings.TrimSpace(remoteRef), "^{}")
	if remoteRef == "" {
		return submittedRef
	}
	return remoteRef
}

func materializeCommit(ctx context.Context, root string, repoURL string, repoKey string, commit string, fetchRef string) error {
	repoSourceRoot := filepath.Join(root, "sources", repoKey)
	if err := os.MkdirAll(repoSourceRoot, 0o755); err != nil {
		return fmt.Errorf("create git selector cache source root: %w", err)
	}

	finalDir := commitSourceDir(root, repoKey, commit)
	if dirExists(finalDir) {
		return nil
	}

	tmpDir, err := os.MkdirTemp(repoSourceRoot, ".tmp-")
	if err != nil {
		return fmt.Errorf("create git selector temp source: %w", err)
	}
	moved := false
	defer func() {
		if !moved {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	if _, err := runGitOutput(ctx, "", "init", tmpDir); err != nil {
		return fmt.Errorf("initialize git selector temp source: %w", err)
	}
	if _, err := runGitOutput(ctx, tmpDir, "remote", "add", "origin", repoURL); err != nil {
		return fmt.Errorf("configure git selector origin %q: %w", repoURL, err)
	}
	if _, err := runGitOutput(ctx, tmpDir, "fetch", "--depth=1", "origin", fetchRef); err != nil {
		if isFullGitHash(fetchRef) {
			return fmt.Errorf("fetch git commit %q from %q with depth 1: %w", fetchRef, repoURL, err)
		}
		return fmt.Errorf("fetch git ref %q from %q with depth 1: %w", fetchRef, repoURL, err)
	}
	if _, err := runGitOutput(ctx, tmpDir, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("checkout git selector source %q: %w", fetchRef, err)
	}
	gotCommit, err := runGitOutput(ctx, tmpDir, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve git selector source commit: %w", err)
	}
	gotCommit = strings.ToLower(strings.TrimSpace(gotCommit))
	if gotCommit != commit {
		return fmt.Errorf("git selector source resolved to %s, want %s", gotCommit, commit)
	}
	if err := writeMetadata(tmpDir, repoURL, commit); err != nil {
		return err
	}

	if dirExists(finalDir) {
		return nil
	}
	if err := os.Rename(tmpDir, finalDir); err != nil {
		if dirExists(finalDir) {
			return nil
		}
		return fmt.Errorf("publish git selector source %q: %w", commit, err)
	}
	moved = true
	return nil
}

func writeMetadata(dir string, repoURL string, commit string) error {
	metadata := sourceMetadata{
		Version:       cacheVersion,
		RepositoryURL: repoURL,
		Commit:        commit,
		CreatedAt:     time.Now().UTC(),
	}
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("encode git selector metadata: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(dir, metadataFile), raw, 0o644); err != nil {
		return fmt.Errorf("write git selector metadata: %w", err)
	}
	return nil
}

func cacheRepoKey(repoURL string) string {
	sum := sha256.Sum256([]byte(repoURL))
	return hex.EncodeToString(sum[:])
}

func commitSourceDir(root string, repoKey string, commit string) string {
	return filepath.Join(root, "sources", repoKey, commit)
}

func dirExists(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && stat.IsDir()
}

func isFullGitHash(ref string) bool {
	ref = strings.TrimSpace(ref)
	if len(ref) != 40 {
		return false
	}
	for _, ch := range ref {
		switch {
		case ch >= '0' && ch <= '9':
		case ch >= 'a' && ch <= 'f':
		case ch >= 'A' && ch <= 'F':
		default:
			return false
		}
	}
	return true
}

func runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}
