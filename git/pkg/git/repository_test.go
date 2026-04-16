package git_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/git/pkg/git"
)

func TestRepositoryLifecycle(t *testing.T) {
	// Create temporary directory for test
	tmpDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	repoPath := filepath.Join(tmpDir, "test-repo")

	// Create repository instance
	repo := git.NewRepository(git.Config{
		DefaultAuthor: "Test User",
		DefaultEmail:  "test@example.com",
	})

	ctx := context.Background()

	// Test 1: Initialize repository
	t.Run("InitRepository", func(t *testing.T) {
		err := repo.InitRepository(ctx, repoPath, git.InitOptions{
			DefaultBranch: "main",
		})
		if err != nil {
			t.Fatalf("Failed to init repository: %v", err)
		}

		// Verify it's a git repo
		status, err := repo.GetStatus(ctx, repoPath)
		if err != nil {
			t.Fatalf("Failed to get status: %v", err)
		}

		if status.Branch != "main" {
			t.Errorf("Expected branch 'main', got '%s'", status.Branch)
		}
	})

	// Test 2: Add and list remotes
	t.Run("AddAndListRemotes", func(t *testing.T) {
		err := repo.AddRemote(ctx, repoPath, "origin", "https://github.com/example/repo.git")
		if err != nil {
			t.Fatalf("Failed to add remote: %v", err)
		}

		remotes, err := repo.ListRemotes(ctx, repoPath)
		if err != nil {
			t.Fatalf("Failed to list remotes: %v", err)
		}

		if len(remotes) != 1 {
			t.Fatalf("Expected 1 remote, got %d", len(remotes))
		}

		if remotes[0].Name != "origin" {
			t.Errorf("Expected remote name 'origin', got '%s'", remotes[0].Name)
		}

		if remotes[0].FetchURL != "https://github.com/example/repo.git" {
			t.Errorf("Expected fetch URL 'https://github.com/example/repo.git', got '%s'", remotes[0].FetchURL)
		}
	})

	// Test 3: Create a commit and get current commit
	t.Run("CreateCommitAndGetCurrent", func(t *testing.T) {
		// Create a test file
		testFile := filepath.Join(repoPath, "test.txt")
		err := os.WriteFile(testFile, []byte("test content"), 0644)
		if err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		// Stage and commit
		err = repo.StageFiles(ctx, repoPath, []string{"test.txt"})
		if err != nil {
			t.Fatalf("Failed to stage file: %v", err)
		}

		err = repo.CreateCommit(ctx, repoPath, "Initial commit")
		if err != nil {
			t.Fatalf("Failed to create commit: %v", err)
		}

		// Get current commit
		commit, err := repo.GetCurrentCommit(ctx, repoPath)
		if err != nil {
			t.Fatalf("Failed to get current commit: %v", err)
		}

		if len(commit) != 40 {
			t.Errorf("Expected 40-character commit hash, got %d characters", len(commit))
		}
	})

	// Test 4: Get file at commit
	t.Run("GetFileAtCommit", func(t *testing.T) {
		commit, err := repo.GetCurrentCommit(ctx, repoPath)
		if err != nil {
			t.Fatalf("Failed to get current commit: %v", err)
		}

		content, err := repo.GetFileAtCommit(ctx, repoPath, commit, "test.txt")
		if err != nil {
			t.Fatalf("Failed to get file at commit: %v", err)
		}

		if string(content) != "test content" {
			t.Errorf("Expected 'test content', got '%s'", string(content))
		}
	})

	// Test 5: List files at commit
	t.Run("ListFilesAtCommit", func(t *testing.T) {
		commit, err := repo.GetCurrentCommit(ctx, repoPath)
		if err != nil {
			t.Fatalf("Failed to get current commit: %v", err)
		}

		files, err := repo.ListFilesAtCommit(ctx, repoPath, commit, "")
		if err != nil {
			t.Fatalf("Failed to list files at commit: %v", err)
		}

		if len(files) != 1 {
			t.Fatalf("Expected 1 file, got %d", len(files))
		}

		if files[0].Path != "test.txt" {
			t.Errorf("Expected file 'test.txt', got '%s'", files[0].Path)
		}

		if files[0].IsDir {
			t.Error("Expected file to not be a directory")
		}
	})

	// Test 6: Checkout
	t.Run("Checkout", func(t *testing.T) {
		// Create a new branch
		err := repo.Checkout(ctx, repoPath, "main", git.CheckoutOptions{
			CreateBranch: "feature-branch",
		})
		if err != nil {
			t.Fatalf("Failed to checkout new branch: %v", err)
		}

		// Verify we're on the new branch
		status, err := repo.GetStatus(ctx, repoPath)
		if err != nil {
			t.Fatalf("Failed to get status: %v", err)
		}

		if status.Branch != "feature-branch" {
			t.Errorf("Expected branch 'feature-branch', got '%s'", status.Branch)
		}

		// Checkout back to main
		err = repo.Checkout(ctx, repoPath, "main", git.CheckoutOptions{})
		if err != nil {
			t.Fatalf("Failed to checkout main: %v", err)
		}

		// Verify we're back on main
		status, err = repo.GetStatus(ctx, repoPath)
		if err != nil {
			t.Fatalf("Failed to get status: %v", err)
		}

		if status.Branch != "main" {
			t.Errorf("Expected branch 'main', got '%s'", status.Branch)
		}
	})

	// Test 7: IsAncestor
	t.Run("IsAncestor", func(t *testing.T) {
		// Get current commit
		commit1, err := repo.GetCurrentCommit(ctx, repoPath)
		if err != nil {
			t.Fatalf("Failed to get current commit: %v", err)
		}

		// Create another commit
		testFile := filepath.Join(repoPath, "test2.txt")
		err = os.WriteFile(testFile, []byte("test content 2"), 0644)
		if err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		err = repo.StageFiles(ctx, repoPath, []string{"test2.txt"})
		if err != nil {
			t.Fatalf("Failed to stage file: %v", err)
		}

		err = repo.CreateCommit(ctx, repoPath, "Second commit")
		if err != nil {
			t.Fatalf("Failed to create commit: %v", err)
		}

		commit2, err := repo.GetCurrentCommit(ctx, repoPath)
		if err != nil {
			t.Fatalf("Failed to get current commit: %v", err)
		}

		// Check if commit1 is ancestor of commit2
		isAncestor, err := repo.IsAncestor(ctx, repoPath, commit1, commit2)
		if err != nil {
			t.Fatalf("Failed to check ancestry: %v", err)
		}

		if !isAncestor {
			t.Error("Expected commit1 to be ancestor of commit2")
		}

		// Check reverse (should be false)
		isAncestor, err = repo.IsAncestor(ctx, repoPath, commit2, commit1)
		if err != nil {
			t.Fatalf("Failed to check ancestry: %v", err)
		}

		if isAncestor {
			t.Error("Expected commit2 to NOT be ancestor of commit1")
		}
	})
}

func TestCloneOptions(t *testing.T) {
	t.Run("CloneOptions structure", func(t *testing.T) {
		depth := 1
		opts := git.CloneOptions{
			Branch:       "main",
			Depth:        &depth,
			SingleBranch: true,
			Bare:         false,
		}

		if opts.Branch != "main" {
			t.Errorf("Expected branch 'main', got '%s'", opts.Branch)
		}

		if opts.Depth == nil || *opts.Depth != 1 {
			t.Error("Expected depth to be 1")
		}

		if !opts.SingleBranch {
			t.Error("Expected SingleBranch to be true")
		}
	})
}

func TestPullOptions(t *testing.T) {
	t.Run("PullOptions structure", func(t *testing.T) {
		opts := git.PullOptions{
			Remote:      "origin",
			Branch:      "main",
			FastForward: true,
			Rebase:      false,
		}

		if opts.Remote != "origin" {
			t.Errorf("Expected remote 'origin', got '%s'", opts.Remote)
		}

		if !opts.FastForward {
			t.Error("Expected FastForward to be true")
		}
	})
}

func TestPushOptions(t *testing.T) {
	t.Run("PushOptions structure", func(t *testing.T) {
		opts := git.PushOptions{
			Remote:      "origin",
			Branch:      "main",
			Force:       false,
			SetUpstream: true,
		}

		if opts.Remote != "origin" {
			t.Errorf("Expected remote 'origin', got '%s'", opts.Remote)
		}

		if opts.SetUpstream != true {
			t.Error("Expected SetUpstream to be true")
		}
	})
}

func TestAuthConfig(t *testing.T) {
	t.Run("AuthConfig structure", func(t *testing.T) {
		auth := git.AuthConfig{
			Type:          git.AuthTypeSSH,
			SSHKeyPath:    "/path/to/key",
			SSHPassword:   "passphrase",
			HTTPSUsername: "",
			HTTPSPassword: "",
		}

		if auth.Type != git.AuthTypeSSH {
			t.Error("Expected AuthTypeSSH")
		}

		if auth.SSHKeyPath != "/path/to/key" {
			t.Errorf("Expected SSHKeyPath '/path/to/key', got '%s'", auth.SSHKeyPath)
		}
	})
}

func TestSparseCheckoutOptions(t *testing.T) {
	t.Run("SparseCheckoutOptions structure", func(t *testing.T) {
		opts := git.SparseCheckoutOptions{
			Cone:  true,
			Paths: []string{".c2/recipes", "docs"},
		}

		if !opts.Cone {
			t.Error("Expected Cone to be true")
		}

		if len(opts.Paths) != 2 {
			t.Errorf("Expected 2 paths, got %d", len(opts.Paths))
		}

		if opts.Paths[0] != ".c2/recipes" {
			t.Errorf("Expected first path '.c2/recipes', got '%s'", opts.Paths[0])
		}
	})

	t.Run("CloneOptions with SparseCheckout", func(t *testing.T) {
		depth := 1
		opts := git.CloneOptions{
			Branch:       "main",
			Depth:        &depth,
			SingleBranch: true,
			SparseCheckout: &git.SparseCheckoutOptions{
				Cone:  true,
				Paths: []string{".c2/recipes"},
			},
		}

		if opts.SparseCheckout == nil {
			t.Fatal("Expected SparseCheckout to not be nil")
		}

		if !opts.SparseCheckout.Cone {
			t.Error("Expected SparseCheckout.Cone to be true")
		}

		if len(opts.SparseCheckout.Paths) != 1 {
			t.Errorf("Expected 1 path, got %d", len(opts.SparseCheckout.Paths))
		}
	})
}

func TestClone_SparseCheckout_Integration(t *testing.T) {
	// Create temporary directory for test
	tmpDir, err := os.MkdirTemp("", "git-sparse-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a source repository with multiple directories
	sourceRepo := filepath.Join(tmpDir, "source")
	repo := git.NewRepository(git.Config{
		DefaultAuthor: "Test User",
		DefaultEmail:  "test@example.com",
	})

	ctx := context.Background()

	// Initialize source repository
	err = repo.InitRepository(ctx, sourceRepo, git.InitOptions{
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("Failed to init source repository: %v", err)
	}

	// Create directory structure
	// .c2/recipes/ - should be checked out with sparse checkout
	// docs/ - should NOT be checked out
	// src/ - should NOT be checked out
	err = os.MkdirAll(filepath.Join(sourceRepo, ".c2", "recipes"), 0755)
	if err != nil {
		t.Fatalf("Failed to create .c2/recipes: %v", err)
	}
	err = os.Mkdir(filepath.Join(sourceRepo, "docs"), 0755)
	if err != nil {
		t.Fatalf("Failed to create docs: %v", err)
	}
	err = os.Mkdir(filepath.Join(sourceRepo, "src"), 0755)
	if err != nil {
		t.Fatalf("Failed to create src: %v", err)
	}

	// Create files in each directory
	err = os.WriteFile(filepath.Join(sourceRepo, ".c2", "recipes", "recipe1.yaml"), []byte("recipe: test"), 0644)
	if err != nil {
		t.Fatalf("Failed to create recipe file: %v", err)
	}
	err = os.WriteFile(filepath.Join(sourceRepo, "docs", "README.md"), []byte("# Documentation"), 0644)
	if err != nil {
		t.Fatalf("Failed to create docs file: %v", err)
	}
	err = os.WriteFile(filepath.Join(sourceRepo, "src", "main.go"), []byte("package main"), 0644)
	if err != nil {
		t.Fatalf("Failed to create src file: %v", err)
	}
	err = os.WriteFile(filepath.Join(sourceRepo, "README.md"), []byte("# Project"), 0644)
	if err != nil {
		t.Fatalf("Failed to create root file: %v", err)
	}

	// Stage and commit all files
	err = repo.StageFiles(ctx, sourceRepo, []string{"."})
	if err != nil {
		t.Fatalf("Failed to stage files: %v", err)
	}

	err = repo.CreateCommit(ctx, sourceRepo, "Initial commit with all files")
	if err != nil {
		t.Fatalf("Failed to create commit: %v", err)
	}

	t.Run("Clone with sparse checkout", func(t *testing.T) {
		cloneDir := filepath.Join(tmpDir, "sparse-clone")
		depth := 1

		err := repo.Clone(ctx, sourceRepo, cloneDir, git.CloneOptions{
			Depth:        &depth,
			SingleBranch: true,
			SparseCheckout: &git.SparseCheckoutOptions{
				Cone:  true,
				Paths: []string{".c2/recipes"},
			},
		})
		if err != nil {
			t.Fatalf("Failed to clone with sparse checkout: %v", err)
		}

		// Verify .c2/recipes exists
		recipePath := filepath.Join(cloneDir, ".c2", "recipes", "recipe1.yaml")
		if _, err := os.Stat(recipePath); os.IsNotExist(err) {
			t.Error("Expected .c2/recipes/recipe1.yaml to exist in sparse checkout")
		}

		// Verify other directories do NOT exist
		docsPath := filepath.Join(cloneDir, "docs")
		if _, err := os.Stat(docsPath); !os.IsNotExist(err) {
			t.Error("Expected docs/ to NOT exist in sparse checkout")
		}

		srcPath := filepath.Join(cloneDir, "src")
		if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
			t.Error("Expected src/ to NOT exist in sparse checkout")
		}

		// Note: In cone mode, parent directories and root-level files are included
		// This is expected behavior of git sparse-checkout --cone
		// So README.md will be present at the root, but docs/ and src/ won't be

		// Verify it's still a valid git repository
		status, err := repo.GetStatus(ctx, cloneDir)
		if err != nil {
			t.Fatalf("Failed to get status of cloned repo: %v", err)
		}

		if !status.Clean {
			t.Error("Expected cloned repository to be clean")
		}
	})

	t.Run("Clone without sparse checkout", func(t *testing.T) {
		cloneDir := filepath.Join(tmpDir, "full-clone")

		err := repo.Clone(ctx, sourceRepo, cloneDir, git.CloneOptions{})
		if err != nil {
			t.Fatalf("Failed to clone without sparse checkout: %v", err)
		}

		// Verify all files exist
		recipePath := filepath.Join(cloneDir, ".c2", "recipes", "recipe1.yaml")
		if _, err := os.Stat(recipePath); os.IsNotExist(err) {
			t.Error("Expected .c2/recipes/recipe1.yaml to exist in full clone")
		}

		docsPath := filepath.Join(cloneDir, "docs", "README.md")
		if _, err := os.Stat(docsPath); os.IsNotExist(err) {
			t.Error("Expected docs/README.md to exist in full clone")
		}

		srcPath := filepath.Join(cloneDir, "src", "main.go")
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			t.Error("Expected src/main.go to exist in full clone")
		}

		readmePath := filepath.Join(cloneDir, "README.md")
		if _, err := os.Stat(readmePath); os.IsNotExist(err) {
			t.Error("Expected README.md to exist in full clone")
		}
	})

	t.Run("Clone with multiple sparse paths", func(t *testing.T) {
		cloneDir := filepath.Join(tmpDir, "multi-sparse-clone")

		err := repo.Clone(ctx, sourceRepo, cloneDir, git.CloneOptions{
			SparseCheckout: &git.SparseCheckoutOptions{
				Cone:  true,
				Paths: []string{".c2/recipes", "docs"},
			},
		})
		if err != nil {
			t.Fatalf("Failed to clone with multiple sparse paths: %v", err)
		}

		// Verify .c2/recipes and docs exist
		recipePath := filepath.Join(cloneDir, ".c2", "recipes", "recipe1.yaml")
		if _, err := os.Stat(recipePath); os.IsNotExist(err) {
			t.Error("Expected .c2/recipes/recipe1.yaml to exist")
		}

		docsPath := filepath.Join(cloneDir, "docs", "README.md")
		if _, err := os.Stat(docsPath); os.IsNotExist(err) {
			t.Error("Expected docs/README.md to exist")
		}

		// Verify src does NOT exist
		srcPath := filepath.Join(cloneDir, "src")
		if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
			t.Error("Expected src/ to NOT exist in sparse checkout")
		}
	})

	t.Run("Clone with empty sparse paths", func(t *testing.T) {
		cloneDir := filepath.Join(tmpDir, "empty-sparse-clone")

		err := repo.Clone(ctx, sourceRepo, cloneDir, git.CloneOptions{
			SparseCheckout: &git.SparseCheckoutOptions{
				Cone:  true,
				Paths: []string{}, // Empty paths - should perform full checkout
			},
		})
		if err != nil {
			t.Fatalf("Failed to clone with empty sparse paths: %v", err)
		}

		// Verify all files exist (full checkout behavior)
		readmePath := filepath.Join(cloneDir, "README.md")
		if _, err := os.Stat(readmePath); os.IsNotExist(err) {
			t.Error("Expected README.md to exist when sparse paths is empty")
		}
	})

	t.Run("Clone with cone mode disabled", func(t *testing.T) {
		cloneDir := filepath.Join(tmpDir, "cone-disabled-clone")

		err := repo.Clone(ctx, sourceRepo, cloneDir, git.CloneOptions{
			SparseCheckout: &git.SparseCheckoutOptions{
				Cone:  false, // Cone disabled - should perform full checkout
				Paths: []string{".c2/recipes"},
			},
		})
		if err != nil {
			t.Fatalf("Failed to clone with cone disabled: %v", err)
		}

		// Verify all files exist (full checkout behavior)
		readmePath := filepath.Join(cloneDir, "README.md")
		if _, err := os.Stat(readmePath); os.IsNotExist(err) {
			t.Error("Expected README.md to exist when cone mode is disabled")
		}
	})
}
