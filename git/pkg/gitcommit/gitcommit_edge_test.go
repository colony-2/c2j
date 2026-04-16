package gitcommit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test negative cases for PersistCommit

func TestPersistCommit_InvalidRepository(t *testing.T) {
	// Test with non-existent repository
	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	input := PersistCommitActivity{
		RepoPath:        "/non/existent/path",
		StorageLocation: storageDir,
		RootHash:        "abc123",
		CommitMessage:   "Test",
	}

	_, err = PersistCommit(context.Background(), input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

func TestPersistCommit_NotAGitRepository(t *testing.T) {
	// Test with a directory that exists but is not a git repository
	tempDir, err := os.MkdirTemp("", "not-git-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	input := PersistCommitActivity{
		RepoPath:        tempDir,
		StorageLocation: storageDir,
		RootHash:        "abc123",
		CommitMessage:   "Test",
	}

	_, err = PersistCommit(context.Background(), input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

func TestPersistCommit_InvalidRootHash(t *testing.T) {
	// Test with root hash that doesn't exist in repository
	repoPath, _, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	input := PersistCommitActivity{
		RepoPath:        repoPath,
		StorageLocation: storageDir,
		RootHash:        "nonexistent12345",
		CommitMessage:   "Test",
	}

	_, err = PersistCommit(context.Background(), input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "root hash")
}

func TestPersistCommit_InvalidStorageLocation(t *testing.T) {
	// Test with storage location that cannot be created
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	input := PersistCommitActivity{
		RepoPath:        repoPath,
		StorageLocation: "/root/forbidden/path",
		RootHash:        rootCommit,
		CommitMessage:   "Test",
	}

	_, err := PersistCommit(context.Background(), input)
	assert.Error(t, err)
}

func TestPersistCommit_NoChangesToCommit(t *testing.T) {
	// Test when there are no changes to commit
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	// Get current commit before attempting persist
	currentCommitBefore := getCommitHash(t, repoPath)

	input := PersistCommitActivity{
		RepoPath:        repoPath,
		StorageLocation: storageDir,
		RootHash:        rootCommit,
		CommitMessage:   "No changes",
	}

	output, err := PersistCommit(context.Background(), input)
	require.NoError(t, err)

	// Should succeed but commit hash should be the same
	assert.Equal(t, currentCommitBefore, output.CommitHash)
	assert.Equal(t, output.CommitHash, output.ParentHash) // No new commit created
	assert.False(t, output.HasChanges)
	assert.Empty(t, output.ThinPackPath)
	assert.Equal(t, int64(0), output.ThinPackSize)
}

func TestPersistCommit_WithTimeout(t *testing.T) {
	// Test timeout handling
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	// Create a file to commit
	testFile := filepath.Join(repoPath, "timeout-test.txt")
	err = os.WriteFile(testFile, []byte("timeout test\n"), 0644)
	require.NoError(t, err)

	input := PersistCommitActivity{
		RepoPath:        repoPath,
		StorageLocation: storageDir,
		RootHash:        rootCommit,
		CommitMessage:   "Timeout test",
		Timeout:         1 * time.Nanosecond, // Very short timeout
	}

	ctx := context.Background()
	_, err = PersistCommit(ctx, input)
	// Should timeout
	assert.Error(t, err)
}

func TestPersistCommit_EmptyCommitMessage(t *testing.T) {
	// Test with empty commit message
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	// Create a file to commit
	testFile := filepath.Join(repoPath, "empty-msg.txt")
	err = os.WriteFile(testFile, []byte("content\n"), 0644)
	require.NoError(t, err)

	input := PersistCommitActivity{
		RepoPath:        repoPath,
		StorageLocation: storageDir,
		RootHash:        rootCommit,
		CommitMessage:   "", // Empty message
	}

	output, err := PersistCommit(context.Background(), input)
	require.NoError(t, err)

	// Should use default message
	assert.NotEmpty(t, output.CommitHash)

	// Verify commit was created with default message
	cmd := exec.Command("git", "log", "-1", "--pretty=%s")
	cmd.Dir = repoPath
	msgOutput, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(msgOutput), "Automated commit")
}

// Test negative cases for RestoreCommit

func TestRestoreCommit_InvalidRepository(t *testing.T) {
	// Test with non-existent repository
	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	input := RestoreCommitActivity{
		RepoPath:        "/non/existent/path",
		TargetCommit:    "abc123",
		RootHash:        "def456",
		StorageLocation: storageDir,
	}

	_, err = RestoreCommit(context.Background(), input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "repository")
}

func TestRestoreCommit_InvalidRootHash(t *testing.T) {
	// Test with root hash that doesn't exist
	repoPath, _, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	input := RestoreCommitActivity{
		RepoPath:        repoPath,
		TargetCommit:    "abc123",
		RootHash:        "nonexistent",
		StorageLocation: storageDir,
	}

	_, err = RestoreCommit(context.Background(), input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "root hash")
}

func TestRestoreCommit_UncommittedChangesNoForce(t *testing.T) {
	// Test restoration with uncommitted changes and force=false
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	// Create uncommitted changes
	testFile := filepath.Join(repoPath, "uncommitted.txt")
	err = os.WriteFile(testFile, []byte("uncommitted\n"), 0644)
	require.NoError(t, err)

	cmd := exec.Command("git", "add", "uncommitted.txt")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	input := RestoreCommitActivity{
		RepoPath:        repoPath,
		TargetCommit:    rootCommit,
		RootHash:        rootCommit,
		StorageLocation: storageDir,
		Force:           false, // Don't force
	}

	_, err = RestoreCommit(context.Background(), input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "uncommitted changes")
}

func TestRestoreCommit_UncommittedChangesWithForce(t *testing.T) {
	// Test restoration with uncommitted changes and force=true
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	// Create uncommitted changes
	testFile := filepath.Join(repoPath, "uncommitted.txt")
	err = os.WriteFile(testFile, []byte("uncommitted\n"), 0644)
	require.NoError(t, err)

	cmd := exec.Command("git", "add", "uncommitted.txt")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	// Note: currentCommit was unused, removing

	input := RestoreCommitActivity{
		RepoPath:        repoPath,
		TargetCommit:    rootCommit,
		RootHash:        rootCommit,
		StorageLocation: storageDir,
		Force:           true, // Force checkout
	}

	output, err := RestoreCommit(context.Background(), input)
	require.NoError(t, err)

	// Should succeed with force
	assert.True(t, output.Success)
	assert.Equal(t, rootCommit, output.CurrentCommit)

	// Uncommitted changes should be gone
	assert.NoFileExists(t, testFile)
}

func TestRestoreCommit_NonExistentTargetCommit(t *testing.T) {
	// Test with target commit that doesn't exist anywhere
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	input := RestoreCommitActivity{
		RepoPath:        repoPath,
		TargetCommit:    "nonexistent123",
		RootHash:        rootCommit,
		StorageLocation: storageDir,
	}

	_, err = RestoreCommit(context.Background(), input)
	assert.Error(t, err)
}

func TestRestoreCommit_MissingThinPacks(t *testing.T) {
	// Test when thin packs directory is empty
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	// Create a new commit
	testFile := filepath.Join(repoPath, "newfile.txt")
	err = os.WriteFile(testFile, []byte("content\n"), 0644)
	require.NoError(t, err)

	cmd := exec.Command("git", "add", ".")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	cmd = exec.Command("git", "commit", "-m", "New commit")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	targetCommit := getCommitHash(t, repoPath)

	// Reset to root
	cmd = exec.Command("git", "reset", "--hard", rootCommit)
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	// Clean up objects
	cmd = exec.Command("git", "reflog", "expire", "--expire=now", "--all")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	cmd = exec.Command("git", "gc", "--prune=now")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	// Try to restore without thin packs
	input := RestoreCommitActivity{
		RepoPath:        repoPath,
		TargetCommit:    targetCommit,
		RootHash:        rootCommit,
		StorageLocation: storageDir, // Empty directory
	}

	_, err = RestoreCommit(context.Background(), input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "thin packs")
}

func TestRestoreCommit_CorruptedThinPack(t *testing.T) {
	// Test with corrupted thin pack file
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	// Create a fake corrupted thin pack
	packName := fmt.Sprintf("fake123-parent456-%s.pack", rootCommit[:7])
	packPath := filepath.Join(storageDir, packName)
	err = os.WriteFile(packPath, []byte("corrupted data"), 0644)
	require.NoError(t, err)

	input := RestoreCommitActivity{
		RepoPath:        repoPath,
		TargetCommit:    "fake123",
		RootHash:        rootCommit,
		StorageLocation: storageDir,
	}

	_, err = RestoreCommit(context.Background(), input)
	assert.Error(t, err)
}

func TestRestoreCommit_WithTimeout(t *testing.T) {
	// Test timeout handling
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	input := RestoreCommitActivity{
		RepoPath:        repoPath,
		TargetCommit:    rootCommit,
		RootHash:        rootCommit,
		StorageLocation: storageDir,
		Timeout:         1 * time.Nanosecond, // Very short timeout
	}

	_, err = RestoreCommit(context.Background(), input)
	assert.Error(t, err)
}

// Test edge cases and boundary conditions

func TestPersistCommit_LargeCommit(t *testing.T) {
	// Test with a large number of files
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	// Create many files
	for i := 0; i < 100; i++ {
		fileName := fmt.Sprintf("file%d.txt", i)
		filePath := filepath.Join(repoPath, fileName)
		content := fmt.Sprintf("Content for file %d\n", i)
		err = os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err)
	}

	cmd := exec.Command("git", "add", ".")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	input := PersistCommitActivity{
		RepoPath:        repoPath,
		StorageLocation: storageDir,
		RootHash:        rootCommit,
		CommitMessage:   "Large commit with 100 files",
	}

	output, err := PersistCommit(context.Background(), input)
	require.NoError(t, err)

	// Should handle large commits
	assert.NotEmpty(t, output.CommitHash)
	assert.Greater(t, output.ThinPackSize, int64(1000)) // Should be reasonably large
}

func TestPersistRestoreRoundTrip(t *testing.T) {
	// Test full round-trip: persist then restore
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	// Create and persist a commit
	testFile := filepath.Join(repoPath, "roundtrip.txt")
	testContent := "Round trip test content\n"
	err = os.WriteFile(testFile, []byte(testContent), 0644)
	require.NoError(t, err)

	persistInput := PersistCommitActivity{
		RepoPath:        repoPath,
		StorageLocation: storageDir,
		RootHash:        rootCommit,
		CommitMessage:   "Round trip test",
	}

	persistOutput, err := PersistCommit(context.Background(), persistInput)
	require.NoError(t, err)

	// Reset to root
	cmd := exec.Command("git", "reset", "--hard", rootCommit)
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	// File should be gone
	assert.NoFileExists(t, testFile)

	// Restore the commit
	restoreInput := RestoreCommitActivity{
		RepoPath:        repoPath,
		TargetCommit:    persistOutput.CommitHash,
		RootHash:        rootCommit,
		StorageLocation: storageDir,
	}

	restoreOutput, err := RestoreCommit(context.Background(), restoreInput)
	require.NoError(t, err)

	// Verify restoration
	assert.True(t, restoreOutput.Success)
	assert.Equal(t, persistOutput.CommitHash, restoreOutput.CurrentCommit)

	// File should be restored with correct content
	assert.FileExists(t, testFile)
	content, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.Equal(t, testContent, string(content))
}

func TestPersistCommit_SpecialCharactersInMessage(t *testing.T) {
	// Test commit message with special characters
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	// Create a file to commit
	testFile := filepath.Join(repoPath, "special.txt")
	err = os.WriteFile(testFile, []byte("content\n"), 0644)
	require.NoError(t, err)

	specialMessages := []string{
		"Message with 'quotes'",
		`Message with "double quotes"`,
		"Message with\nnewlines",
		"Message with emoji 🚀",
		"Message with $pecial ch@rs!",
	}

	for _, msg := range specialMessages {
		input := PersistCommitActivity{
			RepoPath:        repoPath,
			StorageLocation: storageDir,
			RootHash:        rootCommit,
			CommitMessage:   msg,
		}

		output, err := PersistCommit(context.Background(), input)
		require.NoError(t, err, "Failed with message: %s", msg)
		assert.NotEmpty(t, output.CommitHash)
	}
}

func TestRestoreCommit_DetachedHead(t *testing.T) {
	// Test restoration in detached HEAD state
	repoPath, rootCommit, cleanup := setupTestRepo(t)
	defer cleanup()

	storageDir, err := os.MkdirTemp("", "storage-*")
	require.NoError(t, err)
	defer os.RemoveAll(storageDir)

	// Create and persist a commit
	testFile := filepath.Join(repoPath, "detached.txt")
	err = os.WriteFile(testFile, []byte("detached\n"), 0644)
	require.NoError(t, err)

	persistInput := PersistCommitActivity{
		RepoPath:        repoPath,
		StorageLocation: storageDir,
		RootHash:        rootCommit,
		CommitMessage:   "Detached test",
	}

	persistOutput, err := PersistCommit(context.Background(), persistInput)
	require.NoError(t, err)

	// Checkout a specific commit (detached HEAD)
	cmd := exec.Command("git", "checkout", rootCommit)
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	// Verify we're in detached HEAD
	cmd = exec.Command("git", "symbolic-ref", "-q", "HEAD")
	cmd.Dir = repoPath
	err = cmd.Run()
	assert.Error(t, err) // Should fail in detached HEAD

	// Restore should still work
	restoreInput := RestoreCommitActivity{
		RepoPath:        repoPath,
		TargetCommit:    persistOutput.CommitHash,
		RootHash:        rootCommit,
		StorageLocation: storageDir,
	}

	restoreOutput, err := RestoreCommit(context.Background(), restoreInput)
	require.NoError(t, err)
	assert.True(t, restoreOutput.Success)
}
