package thinpackrebase

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunThinpackRebase_ReplaysCommits(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	remotePath := filepath.Join(tempDir, "remote")
	initRepo(t, remotePath)
	baseHash := gitRevParse(t, remotePath, "HEAD")

	workspacePath := filepath.Join(tempDir, "workspace")
	runGit(t, tempDir, "git", "clone", remotePath, workspacePath)
	configureAuthor(t, workspacePath, "Dev User", "dev@example.com")

	appendToFile(t, workspacePath, "app.txt", "workspace change\n")
	runGit(t, workspacePath, "git", "add", "app.txt")
	runGit(t, workspacePath, "git", "commit", "-m", "local change")
	originalPersist := gitRevParse(t, workspacePath, "HEAD")

	appendToFile(t, remotePath, "README.md", "upstream\n")
	runGit(t, remotePath, "git", "add", "README.md")
	runGit(t, remotePath, "git", "commit", "-m", "upstream change")
	targetBase := gitRevParse(t, remotePath, "HEAD")

	input := ThinpackRebaseInput{
		RepoPath:       workspacePath,
		TargetBaseHash: targetBase,
		UpstreamRemote: "origin",
		BaseHash:       baseHash,
		PersistHash:    originalPersist,
		BaseRepo:       remotePath,
		GitAuthor:      "Recipe Bot <bot@example.com>",
		CellName:       "alpha",
	}

	output, err := Run(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, output)

	require.Equal(t, targetBase, output.NewBaseHash)
	require.Equal(t, targetBase, output.TargetBaseHash)
	require.NotEqual(t, originalPersist, output.NewPersistHash)
	require.Equal(t, originalPersist, output.RebasedFrom.PersistHash)
	require.Equal(t, baseHash, output.RebasedFrom.BaseHash)
	require.Equal(t, targetBase, output.GitContextPatch["base_hash"])
	require.Equal(t, output.NewPersistHash, output.GitContextPatch["persist_hash"])
	require.Equal(t, originalPersist, output.GitContextPatch["previous_hash"])

	parent := gitRevParse(t, workspacePath, "HEAD^")
	require.Equal(t, targetBase, parent)
}

func TestRunThinpackRebase_ResetAuthor(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	remotePath := filepath.Join(tempDir, "remote")
	initRepo(t, remotePath)
	baseHash := gitRevParse(t, remotePath, "HEAD")

	workspacePath := filepath.Join(tempDir, "workspace")
	runGit(t, tempDir, "git", "clone", remotePath, workspacePath)
	configureAuthor(t, workspacePath, "Feature Dev", "feature@example.com")

	appendToFile(t, workspacePath, "feature.txt", "change\n")
	runGit(t, workspacePath, "git", "add", "feature.txt")
	runGit(t, workspacePath, "git", "commit", "-m", "feature work")
	originalPersist := gitRevParse(t, workspacePath, "HEAD")

	appendToFile(t, remotePath, "README.md", "upstream\n")
	runGit(t, remotePath, "git", "add", "README.md")
	runGit(t, remotePath, "git", "commit", "-m", "upstream change")
	targetBase := gitRevParse(t, remotePath, "HEAD")

	preserve := false
	input := ThinpackRebaseInput{
		RepoPath:       workspacePath,
		TargetBaseHash: targetBase,
		UpstreamRemote: "origin",
		PreserveAuthor: &preserve,
		BaseHash:       baseHash,
		PersistHash:    originalPersist,
		BaseRepo:       remotePath,
		GitAuthor:      "Workflow Bot <workflow@example.com>",
		CellName:       "workflow",
	}

	output, err := Run(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, output)

	author := strings.TrimSpace(runGitOutput(t, workspacePath, "git", "log", "-1", "--format=%an <%ae>"))
	require.Equal(t, "Workflow Bot <workflow@example.com>", author)
}

func TestRunThinpackRebase_FastForwardWhenNoLocalCommits(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	remotePath := filepath.Join(tempDir, "remote")
	initRepo(t, remotePath)
	baseHash := gitRevParse(t, remotePath, "HEAD")

	workspacePath := filepath.Join(tempDir, "workspace")
	runGit(t, tempDir, "git", "clone", remotePath, workspacePath)
	configureAuthor(t, workspacePath, "Dev User", "dev@example.com")

	appendToFile(t, remotePath, "README.md", "upstream\n")
	runGit(t, remotePath, "git", "add", "README.md")
	runGit(t, remotePath, "git", "commit", "-m", "upstream change")
	targetBase := gitRevParse(t, remotePath, "HEAD")

	input := ThinpackRebaseInput{
		RepoPath:       workspacePath,
		TargetBaseHash: targetBase,
		UpstreamRemote: "origin",
		BaseHash:    baseHash,
		PersistHash: baseHash,
		BaseRepo:    remotePath,
	}

	output, err := Run(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, output)

	head := gitRevParse(t, workspacePath, "HEAD")
	require.Equal(t, targetBase, head)
	require.Equal(t, targetBase, output.GitContextPatch["base_hash"])
	require.Equal(t, targetBase, output.GitContextPatch["persist_hash"])
}

func TestRunThinpackRebase_FailsWhenDiverged(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	remotePath := filepath.Join(tempDir, "remote")
	initRepo(t, remotePath)
	appendToFile(t, remotePath, "README.md", "main\n")
	runGit(t, remotePath, "git", "add", "README.md")
	runGit(t, remotePath, "git", "commit", "-m", "main update")
	baseHash := gitRevParse(t, remotePath, "HEAD")

	// create alternative history not descending from base
	ancestor := gitRevParse(t, remotePath, "HEAD^")
	runGit(t, remotePath, "git", "checkout", "-b", "alt", ancestor)
	appendToFile(t, remotePath, "alt.txt", "alt\n")
	runGit(t, remotePath, "git", "add", "alt.txt")
	runGit(t, remotePath, "git", "commit", "-m", "alternate")
	divergent := gitRevParse(t, remotePath, "HEAD")
	runGit(t, remotePath, "git", "checkout", "main")

	workspacePath := filepath.Join(tempDir, "workspace")
	runGit(t, tempDir, "git", "clone", remotePath, workspacePath)
	configureAuthor(t, workspacePath, "Dev User", "dev@example.com")

	appendToFile(t, workspacePath, "feature.txt", "change\n")
	runGit(t, workspacePath, "git", "add", "feature.txt")
	runGit(t, workspacePath, "git", "commit", "-m", "feature")
	persist := gitRevParse(t, workspacePath, "HEAD")

	input := ThinpackRebaseInput{
		RepoPath:       workspacePath,
		TargetBaseHash: divergent,
		UpstreamRemote: "origin",
		BaseHash:    baseHash,
		PersistHash: persist,
		BaseRepo:    remotePath,
	}

	_, err := Run(context.Background(), input)
	require.Error(t, err)
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, filepath.Dir(dir), "git", "init", dir)
	configureAuthor(t, dir, "Main Dev", "main@example.com")
	appendToFile(t, dir, "README.md", "base\n")
	runGit(t, dir, "git", "add", "README.md")
	runGit(t, dir, "git", "commit", "-m", "initial")
	runGit(t, dir, "git", "checkout", "-B", "main")
}

func configureAuthor(t *testing.T, dir, name, email string) {
	t.Helper()
	runGit(t, dir, "git", "config", "user.name", name)
	runGit(t, dir, "git", "config", "user.email", email)
}

func appendToFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open file: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func runGit(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v (%s)", args, err, output)
	}
}

func runGitOutput(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v (%s)", args, err, output)
	}
	return string(output)
}

func gitRevParse(t *testing.T, dir, ref string) string {
	out := runGitOutput(t, dir, "git", "rev-parse", ref)
	return strings.TrimSpace(out)
}
