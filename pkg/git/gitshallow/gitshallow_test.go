package gitshallow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClone(t *testing.T) {
	ctx := context.Background()

	// Create a temporary directory for tests
	tempDir, err := os.MkdirTemp("", "git-clone-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a test git repository
	sourceDir := filepath.Join(tempDir, "source-repo")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to init git repo: %v", err)
	}

	// Configure git user for the test repo
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to configure git user email: %v", err)
	}

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to configure git user name: %v", err)
	}

	// Create first commit
	testFile := filepath.Join(sourceDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to add files: %v", err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Create second commit
	if err := os.WriteFile(testFile, []byte("second content"), 0644); err != nil {
		t.Fatalf("Failed to update test file: %v", err)
	}

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to add files: %v", err)
	}

	cmd = exec.Command("git", "commit", "-m", "Second commit")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Create third commit
	if err := os.WriteFile(testFile, []byte("third content"), 0644); err != nil {
		t.Fatalf("Failed to update test file: %v", err)
	}

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to add files: %v", err)
	}

	cmd = exec.Command("git", "commit", "-m", "Third commit")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Get the latest commit hash
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = sourceDir
	commitHashBytes, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get commit hash: %v", err)
	}
	commitHash := string(commitHashBytes[:len(commitHashBytes)-1]) // Remove newline

	t.Run("successful clone", func(t *testing.T) {
		targetDir := filepath.Join(tempDir, "target-repo")
		input := GitShallowCloneInput{
			SourceDir:  sourceDir,
			TargetDir:  targetDir,
			CommitHash: commitHash,
		}

		output, err := GitShallowClone(ctx, input)
		if err != nil {
			t.Fatalf("Clone failed: %v", err)
		}

		if output.ClonedPath != targetDir {
			t.Errorf("Expected cloned path %s, got %s", targetDir, output.ClonedPath)
		}

		// Verify the clone exists and has the correct commit
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = targetDir
		clonedHashBytes, err := cmd.Output()
		if err != nil {
			t.Fatalf("Failed to get cloned commit hash: %v", err)
		}
		clonedHash := string(clonedHashBytes[:len(clonedHashBytes)-1])

		if clonedHash != commitHash {
			t.Errorf("Expected commit hash %s, got %s", commitHash, clonedHash)
		}

		// Verify the test file exists
		clonedTestFile := filepath.Join(targetDir, "test.txt")
		if _, err := os.Stat(clonedTestFile); err != nil {
			t.Errorf("Test file not found in clone: %v", err)
		}

		// Verify the clone is shallow (should only have depth of 1)
		cmd = exec.Command("git", "rev-list", "--count", "HEAD")
		cmd.Dir = targetDir
		depthBytes, err := cmd.Output()
		if err != nil {
			t.Fatalf("Failed to get commit count: %v", err)
		}
		depth := string(depthBytes[:len(depthBytes)-1])

		if depth != "1" {
			t.Errorf("Expected shallow clone with depth 1, got depth %s", depth)
		}

		// Verify we can't access parent commits
		cmd = exec.Command("git", "log", "--oneline")
		cmd.Dir = targetDir
		logOutput, err := cmd.Output()
		if err != nil {
			t.Fatalf("Failed to get git log: %v", err)
		}

		logLines := string(logOutput)
		// Should only contain one commit (the one we checked out)
		if strings.Count(logLines, "\n") > 1 {
			t.Errorf("Expected only one commit in shallow clone, got:\n%s", logLines)
		}
	})

	t.Run("remote source file URL clone", func(t *testing.T) {
		targetDir := filepath.Join(tempDir, "target-remote-file-url")
		input := GitShallowCloneInput{
			SourceDir:  "file://" + sourceDir,
			TargetDir:  targetDir,
			CommitHash: commitHash,
		}

		output, err := GitShallowClone(ctx, input)
		if err != nil {
			t.Fatalf("Remote file URL clone failed: %v", err)
		}

		if output.ClonedPath != targetDir {
			t.Fatalf("remote clone: expected cloned path %s, got %s", targetDir, output.ClonedPath)
		}

		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = targetDir
		clonedHashBytes, err := cmd.Output()
		if err != nil {
			t.Fatalf("Failed to get cloned commit hash for remote clone: %v", err)
		}
		clonedHash := string(clonedHashBytes[:len(clonedHashBytes)-1])
		if clonedHash != commitHash {
			t.Fatalf("remote clone: expected commit hash %s, got %s", commitHash, clonedHash)
		}
	})

	t.Run("empty source directory", func(t *testing.T) {
		targetDir := filepath.Join(tempDir, "target-empty-source")
		input := GitShallowCloneInput{
			SourceDir:  "",
			TargetDir:  targetDir,
			CommitHash: commitHash,
		}

		_, err := GitShallowClone(ctx, input)
		if err == nil {
			t.Error("Expected error for empty source directory, got nil")
		}
		if err.Error() != "source directory cannot be empty" {
			t.Errorf("Unexpected error message: %v", err)
		}
	})

	t.Run("empty target directory", func(t *testing.T) {
		input := GitShallowCloneInput{
			SourceDir:  sourceDir,
			TargetDir:  "",
			CommitHash: commitHash,
		}

		_, err := GitShallowClone(ctx, input)
		if err == nil {
			t.Error("Expected error for empty target directory, got nil")
		}
		if err.Error() != "target directory cannot be empty" {
			t.Errorf("Unexpected error message: %v", err)
		}
	})

	t.Run("empty commit hash", func(t *testing.T) {
		targetDir := filepath.Join(tempDir, "target-empty-hash")
		input := GitShallowCloneInput{
			SourceDir:  sourceDir,
			TargetDir:  targetDir,
			CommitHash: "",
		}

		_, err := GitShallowClone(ctx, input)
		if err == nil {
			t.Error("Expected error for empty commit hash, got nil")
		}
		if err.Error() != "commit hash cannot be empty" {
			t.Errorf("Unexpected error message: %v", err)
		}
	})

	t.Run("non-existent source directory", func(t *testing.T) {
		targetDir := filepath.Join(tempDir, "target-nonexistent-source")
		input := GitShallowCloneInput{
			SourceDir:  filepath.Join(tempDir, "nonexistent"),
			TargetDir:  targetDir,
			CommitHash: commitHash,
		}

		_, err := GitShallowClone(ctx, input)
		if err == nil {
			t.Error("Expected error for non-existent source directory, got nil")
		}
	})

	t.Run("source directory not a git repo", func(t *testing.T) {
		nonGitDir := filepath.Join(tempDir, "not-git")
		if err := os.MkdirAll(nonGitDir, 0755); err != nil {
			t.Fatalf("Failed to create non-git dir: %v", err)
		}

		targetDir := filepath.Join(tempDir, "target-not-git")
		input := GitShallowCloneInput{
			SourceDir:  nonGitDir,
			TargetDir:  targetDir,
			CommitHash: commitHash,
		}

		_, err := GitShallowClone(ctx, input)
		if err == nil {
			t.Error("Expected error for non-git source directory, got nil")
		}
	})
}
