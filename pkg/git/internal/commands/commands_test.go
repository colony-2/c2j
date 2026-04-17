package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitStatus(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Create a test node directory
	nodeDir := filepath.Join(tempDir, "test-node")
	err = os.Mkdir(nodeDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize git repository
	cmd := exec.Command("git", "init")
	cmd.Dir = nodeDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Configure git
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = nodeDir
	cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = nodeDir
	cmd.Run()

	// Create repository instance
	repo := New("test@example.com", "Test User")
	ctx := context.Background()

	// Test clean repository
	status, err := repo.GetStatus(ctx, nodeDir)
	if err != nil {
		t.Fatalf("Failed to get status: %v", err)
	}

	if !status.Clean {
		t.Error("Expected clean repository")
	}

	if len(status.Files) != 0 {
		t.Errorf("Expected 0 files, got %d", len(status.Files))
	}

	// Create a test file
	testFile := filepath.Join(nodeDir, "test.txt")
	err = os.WriteFile(testFile, []byte("test content"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Test with untracked file
	status, err = repo.GetStatus(ctx, nodeDir)
	if err != nil {
		t.Fatalf("Failed to get status: %v", err)
	}

	if status.Clean {
		t.Error("Expected repository to not be clean")
	}

	if len(status.Files) != 1 {
		t.Errorf("Expected 1 file, got %d", len(status.Files))
	}

	if len(status.Files) > 0 && status.Files[0].Status != "?" {
		t.Errorf("Expected untracked status '?', got %s", status.Files[0].Status)
	}

	// Stage the file
	err = repo.StageFiles(ctx, nodeDir, []string{"test.txt"})
	if err != nil {
		t.Fatalf("Failed to stage file: %v", err)
	}

	// Test with staged file
	status, err = repo.GetStatus(ctx, nodeDir)
	if err != nil {
		t.Fatalf("Failed to get status: %v", err)
	}

	if len(status.Files) != 1 {
		t.Errorf("Expected 1 file, got %d", len(status.Files))
	}

	if len(status.Files) > 0 && status.Files[0].Status != "A" {
		t.Errorf("Expected added status 'A', got %s", status.Files[0].Status)
	}
}

func TestGitCommit(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Create a test node directory
	nodeDir := filepath.Join(tempDir, "test-node")
	err = os.Mkdir(nodeDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize git repository
	cmd := exec.Command("git", "init")
	cmd.Dir = nodeDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Configure git
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = nodeDir
	cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = nodeDir
	cmd.Run()

	// Create and stage a test file
	testFile := filepath.Join(nodeDir, "test.txt")
	err = os.WriteFile(testFile, []byte("test content"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create repository instance
	repo := New("test@example.com", "Test User")
	ctx := context.Background()

	// Stage the file
	err = repo.StageFiles(ctx, nodeDir, []string{"test.txt"})
	if err != nil {
		t.Fatalf("Failed to stage file: %v", err)
	}

	// Create commit
	err = repo.CreateCommit(ctx, nodeDir, "Test commit")
	if err != nil {
		t.Fatalf("Failed to create commit: %v", err)
	}

	// Verify commit was created
	history, err := repo.GetHistory(ctx, nodeDir, 1)
	if err != nil {
		t.Fatalf("Failed to get history: %v", err)
	}

	if len(history) != 1 {
		t.Errorf("Expected 1 commit, got %d", len(history))
	}

	if len(history) > 0 && !strings.Contains(history[0].Message, "Test commit") {
		t.Errorf("Expected commit message to contain 'Test commit', got %s", history[0].Message)
	}
}

func TestGitDiff(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Create a test node directory
	nodeDir := filepath.Join(tempDir, "test-node")
	err = os.Mkdir(nodeDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize git repository
	cmd := exec.Command("git", "init")
	cmd.Dir = nodeDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Configure git
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = nodeDir
	cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = nodeDir
	cmd.Run()

	// Create and commit initial file
	testFile := filepath.Join(nodeDir, "test.txt")
	err = os.WriteFile(testFile, []byte("initial content\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = nodeDir
	cmd.Run()

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = nodeDir
	cmd.Run()

	// Modify the file
	err = os.WriteFile(testFile, []byte("initial content\nmodified content\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create repository instance
	repo := New("test@example.com", "Test User")
	ctx := context.Background()

	// Get unstaged diff
	diff, err := repo.GetDiff(ctx, nodeDir, false)
	if err != nil {
		t.Fatalf("Failed to get diff: %v", err)
	}

	if !strings.Contains(diff, "+modified content") {
		t.Error("Expected diff to contain '+modified content'")
	}

	// Stage the changes
	err = repo.StageFiles(ctx, nodeDir, []string{"test.txt"})
	if err != nil {
		t.Fatalf("Failed to stage file: %v", err)
	}

	// Get staged diff
	diff, err = repo.GetDiff(ctx, nodeDir, true)
	if err != nil {
		t.Fatalf("Failed to get staged diff: %v", err)
	}

	if !strings.Contains(diff, "+modified content") {
		t.Error("Expected staged diff to contain '+modified content'")
	}
}

func TestNonGitRepository(t *testing.T) {
	// Create a temporary directory without git
	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Create repository instance
	repo := New("test@example.com", "Test User")
	ctx := context.Background()

	// All operations should return appropriate errors
	_, err = repo.GetStatus(ctx, tempDir)
	if err == nil {
		t.Error("Expected error for non-git directory")
	}

	_, err = repo.GetDiff(ctx, tempDir, false)
	if err == nil {
		t.Error("Expected error for non-git directory")
	}

	_, err = repo.GetHistory(ctx, tempDir, 10)
	if err == nil {
		t.Error("Expected error for non-git directory")
	}

	err = repo.CreateCommit(ctx, tempDir, "Test")
	if err == nil {
		t.Error("Expected error for non-git directory")
	}
}

func TestClone_WithSparseCheckout(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Create a source repository with multiple directories
	sourceDir := filepath.Join(tempDir, "source-repo")
	err = os.Mkdir(sourceDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize git repository
	cmd := exec.Command("git", "init")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Configure git
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = sourceDir
	cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = sourceDir
	cmd.Run()

	// Create directory structure with multiple paths
	// .c2/recipes/ - should be checked out
	// docs/ - should NOT be checked out
	// src/ - should NOT be checked out
	err = os.MkdirAll(filepath.Join(sourceDir, ".c2", "recipes"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.Mkdir(filepath.Join(sourceDir, "docs"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.Mkdir(filepath.Join(sourceDir, "src"), 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Create files in each directory
	err = os.WriteFile(filepath.Join(sourceDir, ".c2", "recipes", "recipe1.txt"), []byte("recipe content"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(sourceDir, "docs", "readme.txt"), []byte("docs content"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(sourceDir, "src", "main.go"), []byte("source code"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(sourceDir, "root.txt"), []byte("root content"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Stage and commit all files
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create repository instance
	repo := New("test@example.com", "Test User")
	ctx := context.Background()

	// Test sparse checkout clone
	cloneDir := filepath.Join(tempDir, "sparse-clone")
	depth := 1
	err = repo.Clone(ctx, sourceDir, cloneDir, "", &depth, true, false, &SparseCheckoutOptions{
		Cone:  true,
		Paths: []string{".c2/recipes"},
	})
	if err != nil {
		t.Fatalf("Failed to clone with sparse checkout: %v", err)
	}

	// Verify that .c2/recipes exists
	recipePath := filepath.Join(cloneDir, ".c2", "recipes", "recipe1.txt")
	if _, err := os.Stat(recipePath); os.IsNotExist(err) {
		t.Error("Expected .c2/recipes/recipe1.txt to exist in sparse checkout")
	}

	// Verify that docs directory does NOT exist (sparse checkout)
	docsPath := filepath.Join(cloneDir, "docs")
	if _, err := os.Stat(docsPath); !os.IsNotExist(err) {
		t.Error("Expected docs/ to NOT exist in sparse checkout")
	}

	// Verify that src directory does NOT exist (sparse checkout)
	srcPath := filepath.Join(cloneDir, "src")
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Error("Expected src/ to NOT exist in sparse checkout")
	}

	// Note: In cone mode, parent directories and root-level files are included
	// This is expected behavior of git sparse-checkout --cone
	// So root.txt will be present, but docs/ and src/ directories won't be

	// Verify it's a valid git repository
	if !isGitRepo(cloneDir) {
		t.Error("Expected cloned directory to be a git repository")
	}
}

func TestClone_WithoutSparseCheckout(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Create a source repository
	sourceDir := filepath.Join(tempDir, "source-repo")
	err = os.Mkdir(sourceDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize git repository
	cmd := exec.Command("git", "init")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Configure git
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = sourceDir
	cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = sourceDir
	cmd.Run()

	// Create test file
	err = os.WriteFile(filepath.Join(sourceDir, "test.txt"), []byte("test content"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Stage and commit
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create repository instance
	repo := New("test@example.com", "Test User")
	ctx := context.Background()

	// Test normal clone (nil sparse checkout)
	cloneDir := filepath.Join(tempDir, "normal-clone")
	err = repo.Clone(ctx, sourceDir, cloneDir, "", nil, false, false, nil)
	if err != nil {
		t.Fatalf("Failed to clone: %v", err)
	}

	// Verify file exists
	testPath := filepath.Join(cloneDir, "test.txt")
	if _, err := os.Stat(testPath); os.IsNotExist(err) {
		t.Error("Expected test.txt to exist in normal clone")
	}

	// Verify it's a valid git repository
	if !isGitRepo(cloneDir) {
		t.Error("Expected cloned directory to be a git repository")
	}
}

func TestClone_SparseCheckoutEmptyPaths(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Create a source repository
	sourceDir := filepath.Join(tempDir, "source-repo")
	err = os.Mkdir(sourceDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize git repository
	cmd := exec.Command("git", "init")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Configure git
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = sourceDir
	cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = sourceDir
	cmd.Run()

	// Create test file
	err = os.WriteFile(filepath.Join(sourceDir, "test.txt"), []byte("test content"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Stage and commit
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create repository instance
	repo := New("test@example.com", "Test User")
	ctx := context.Background()

	// Test clone with empty paths (should behave like normal clone)
	cloneDir := filepath.Join(tempDir, "empty-paths-clone")
	err = repo.Clone(ctx, sourceDir, cloneDir, "", nil, false, false, &SparseCheckoutOptions{
		Cone:  true,
		Paths: []string{}, // Empty paths
	})
	if err != nil {
		t.Fatalf("Failed to clone with empty paths: %v", err)
	}

	// Verify file exists (full checkout)
	testPath := filepath.Join(cloneDir, "test.txt")
	if _, err := os.Stat(testPath); os.IsNotExist(err) {
		t.Error("Expected test.txt to exist when sparse checkout has empty paths")
	}
}

func TestClone_SparseCheckoutConeDisabled(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Create a source repository
	sourceDir := filepath.Join(tempDir, "source-repo")
	err = os.Mkdir(sourceDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize git repository
	cmd := exec.Command("git", "init")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Configure git
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = sourceDir
	cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = sourceDir
	cmd.Run()

	// Create test file
	err = os.WriteFile(filepath.Join(sourceDir, "test.txt"), []byte("test content"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Stage and commit
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create repository instance
	repo := New("test@example.com", "Test User")
	ctx := context.Background()

	// Test clone with Cone=false (should behave like normal clone)
	cloneDir := filepath.Join(tempDir, "cone-disabled-clone")
	err = repo.Clone(ctx, sourceDir, cloneDir, "", nil, false, false, &SparseCheckoutOptions{
		Cone:  false, // Cone disabled
		Paths: []string{".c2/recipes"},
	})
	if err != nil {
		t.Fatalf("Failed to clone with cone disabled: %v", err)
	}

	// Verify file exists (full checkout)
	testPath := filepath.Join(cloneDir, "test.txt")
	if _, err := os.Stat(testPath); os.IsNotExist(err) {
		t.Error("Expected test.txt to exist when cone mode is disabled")
	}
}

func TestClone_SparseCheckoutMultiplePaths(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Create a source repository with multiple directories
	sourceDir := filepath.Join(tempDir, "source-repo")
	err = os.Mkdir(sourceDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Initialize git repository
	cmd := exec.Command("git", "init")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Configure git
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = sourceDir
	cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = sourceDir
	cmd.Run()

	// Create directory structure
	err = os.Mkdir(filepath.Join(sourceDir, "docs"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.Mkdir(filepath.Join(sourceDir, "config"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.Mkdir(filepath.Join(sourceDir, "src"), 0755)
	if err != nil {
		t.Fatal(err)
	}

	// Create files
	err = os.WriteFile(filepath.Join(sourceDir, "docs", "readme.txt"), []byte("docs"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(sourceDir, "config", "app.json"), []byte("config"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(sourceDir, "src", "main.go"), []byte("source"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Stage and commit
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = sourceDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	// Create repository instance
	repo := New("test@example.com", "Test User")
	ctx := context.Background()

	// Test sparse checkout with multiple paths
	cloneDir := filepath.Join(tempDir, "multi-path-clone")
	err = repo.Clone(ctx, sourceDir, cloneDir, "", nil, false, false, &SparseCheckoutOptions{
		Cone:  true,
		Paths: []string{"docs", "config"},
	})
	if err != nil {
		t.Fatalf("Failed to clone with multiple sparse paths: %v", err)
	}

	// Verify docs exists
	docsPath := filepath.Join(cloneDir, "docs", "readme.txt")
	if _, err := os.Stat(docsPath); os.IsNotExist(err) {
		t.Error("Expected docs/readme.txt to exist in sparse checkout")
	}

	// Verify config exists
	configPath := filepath.Join(cloneDir, "config", "app.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("Expected config/app.json to exist in sparse checkout")
	}

	// Verify src does NOT exist
	srcPath := filepath.Join(cloneDir, "src")
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Error("Expected src/ to NOT exist in sparse checkout")
	}
}
