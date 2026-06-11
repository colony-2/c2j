package selectorcache

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestResolveMutableRefMaterializesShallowSource(t *testing.T) {
	repoDir := newTestRepo(t)
	first := commitFile(t, repoDir, "file.txt", "first\n", "first")
	second := commitFile(t, repoDir, "file.txt", "second\n", "second")
	if first == second {
		t.Fatal("test fixture did not create distinct commits")
	}

	cache := &Cache{Root: t.TempDir()}
	result, err := cache.Resolve(context.Background(), ResolveRequest{
		RepositoryURL: fileURL(repoDir),
		Ref:           "main",
	})
	if err != nil {
		t.Fatalf("resolve selector source: %v", err)
	}
	if result.Commit != second {
		t.Fatalf("commit = %q, want %q", result.Commit, second)
	}
	if result.SourceDir != filepath.Join(cache.Root, "sources", result.RepoKey, second) {
		t.Fatalf("unexpected source dir %q", result.SourceDir)
	}
	if got := readFile(t, filepath.Join(result.SourceDir, "file.txt")); got != "second\n" {
		t.Fatalf("cached file content = %q", got)
	}
	if got := runTestGitOutput(t, result.SourceDir, "rev-parse", "--is-shallow-repository"); got != "true\n" {
		t.Fatalf("cached source should be shallow, got %q", got)
	}
}

func TestResolvePinnedCommitUsesExistingSourceWithoutRemote(t *testing.T) {
	repoDir := newTestRepo(t)
	commit := commitFile(t, repoDir, "file.txt", "cached\n", "cached")

	cache := &Cache{Root: t.TempDir()}
	first, err := cache.Resolve(context.Background(), ResolveRequest{
		RepositoryURL: fileURL(repoDir),
		Ref:           "main",
	})
	if err != nil {
		t.Fatalf("resolve mutable selector source: %v", err)
	}
	if first.Commit != commit {
		t.Fatalf("commit = %q, want %q", first.Commit, commit)
	}

	goneRepo := repoDir + ".gone"
	if err := os.Rename(repoDir, goneRepo); err != nil {
		t.Fatalf("rename remote repo away: %v", err)
	}

	second, err := cache.Resolve(context.Background(), ResolveRequest{
		RepositoryURL: fileURL(repoDir),
		Ref:           commit,
	})
	if err != nil {
		t.Fatalf("resolve pinned selector source from cache: %v", err)
	}
	if second.SourceDir != first.SourceDir {
		t.Fatalf("source dir = %q, want %q", second.SourceDir, first.SourceDir)
	}
}

func TestResolveMutableRefTracksMovedBranch(t *testing.T) {
	repoDir := newTestRepo(t)
	firstCommit := commitFile(t, repoDir, "file.txt", "first\n", "first")

	cache := &Cache{Root: t.TempDir()}
	first, err := cache.Resolve(context.Background(), ResolveRequest{
		RepositoryURL: fileURL(repoDir),
		Ref:           "main",
	})
	if err != nil {
		t.Fatalf("resolve first selector source: %v", err)
	}
	if first.Commit != firstCommit {
		t.Fatalf("first commit = %q, want %q", first.Commit, firstCommit)
	}

	secondCommit := commitFile(t, repoDir, "file.txt", "second\n", "second")
	second, err := cache.Resolve(context.Background(), ResolveRequest{
		RepositoryURL: fileURL(repoDir),
		Ref:           "main",
	})
	if err != nil {
		t.Fatalf("resolve moved selector source: %v", err)
	}
	if second.Commit != secondCommit {
		t.Fatalf("second commit = %q, want %q", second.Commit, secondCommit)
	}
	if second.SourceDir == first.SourceDir {
		t.Fatalf("branch move reused old source dir %q", second.SourceDir)
	}
}

func TestResolveConcurrentSameCommitProducesOneFinalSource(t *testing.T) {
	repoDir := newTestRepo(t)
	commit := commitFile(t, repoDir, "file.txt", "concurrent\n", "concurrent")

	cache := &Cache{Root: t.TempDir()}
	const workers = 8
	var wg sync.WaitGroup
	results := make(chan ResolveResult, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := cache.Resolve(context.Background(), ResolveRequest{
				RepositoryURL: fileURL(repoDir),
				Ref:           commit,
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("resolve concurrent selector source: %v", err)
	}

	var sourceDir string
	count := 0
	for result := range results {
		count++
		if result.Commit != commit {
			t.Fatalf("commit = %q, want %q", result.Commit, commit)
		}
		if sourceDir == "" {
			sourceDir = result.SourceDir
		} else if result.SourceDir != sourceDir {
			t.Fatalf("source dir = %q, want %q", result.SourceDir, sourceDir)
		}
	}
	if count != workers {
		t.Fatalf("got %d results, want %d", count, workers)
	}

	parent := filepath.Dir(sourceDir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read cache parent: %v", err)
	}
	finals := 0
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() == commit {
			finals++
		}
		if entry.IsDir() && len(entry.Name()) >= 5 && entry.Name()[:5] == ".tmp-" {
			t.Fatalf("temporary directory was not cleaned up: %s", entry.Name())
		}
	}
	if finals != 1 {
		t.Fatalf("final commit dirs = %d, want 1", finals)
	}
}

func TestResolvePinnedCommitFetchFailureIsClear(t *testing.T) {
	repoDir := newTestRepo(t)
	commitFile(t, repoDir, "file.txt", "one\n", "one")

	cache := &Cache{Root: t.TempDir()}
	_, err := cache.Resolve(context.Background(), ResolveRequest{
		RepositoryURL: fileURL(repoDir),
		Ref:           "1111111111111111111111111111111111111111",
	})
	if err == nil {
		t.Fatal("expected fetch failure")
	}
	if got := err.Error(); !strings.Contains(got, "fetch git commit") {
		t.Fatalf("expected clear fetch-by-SHA error, got %v", err)
	}
}

func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runTestGit(t, "", "init", "--initial-branch", "main", dir)
	runTestGit(t, dir, "config", "user.email", "selector-cache-test@example.com")
	runTestGit(t, dir, "config", "user.name", "Selector Cache Test")
	return dir
}

func commitFile(t *testing.T, repoDir string, name string, content string, message string) string {
	t.Helper()
	path := filepath.Join(repoDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir file parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runTestGit(t, repoDir, "add", ".")
	runTestGit(t, repoDir, "commit", "-m", message)
	return strings.TrimSpace(runTestGitOutput(t, repoDir, "rev-parse", "HEAD"))
}

func fileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = runTestGitOutput(t, dir, args...)
}

func runTestGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", fmt.Sprint(args), err, out)
	}
	return string(out)
}
