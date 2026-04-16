package gitcollector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/git/internal/commands"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestGitRepo creates a temporary git repository for testing
func setupTestGitRepo(t *testing.T, files map[string]string) string {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "git-test-*")
	require.NoError(t, err)

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	err = cmd.Run()
	require.NoError(t, err)

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = tmpDir
	err = cmd.Run()
	require.NoError(t, err)

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	err = cmd.Run()
	require.NoError(t, err)

	// Create and commit files
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		dir := filepath.Dir(fullPath)
		if dir != tmpDir {
			err = os.MkdirAll(dir, 0755)
			require.NoError(t, err)
		}
		err = os.WriteFile(fullPath, []byte(content), 0644)
		require.NoError(t, err)
	}

	// Add all files
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	err = cmd.Run()
	require.NoError(t, err)

	// Commit
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = tmpDir
	err = cmd.Run()
	require.NoError(t, err)

	return tmpDir
}

func TestGitFileCollectorActivity(t *testing.T) {

	activity := &gitFileCollectorActivity{gitRepo: commands.New("", "")}
	t.Run("CollectTrackedFiles", func(t *testing.T) {
		tmpDir := setupTestGitRepo(t, map[string]string{
			"main.go":     "package main\n\nfunc main() {}\n",
			"lib/util.go": "package lib\n\nfunc Util() {}\n",
			"README.md":   "# Test Project\n",
		})
		defer os.RemoveAll(tmpDir)

		input := GitFileCollectorInput{
			ContextDir: tmpDir,
		}

		output, err := activity.Execute(nil, context.Background(), input)
		require.NoError(t, err)
		assert.Equal(t, 3, output.FileCount)
		assert.NotEmpty(t, output.Repository.CommitHash)
		assert.Equal(t, 3, len(output.Files))

		// Check that files have correct content
		fileMap := make(map[string]string)
		for _, f := range output.Files {
			fileMap[f.Path] = string(f.Content)
		}
		assert.Equal(t, "package main\n\nfunc main() {}\n", fileMap["main.go"])
		assert.Equal(t, "package lib\n\nfunc Util() {}\n", fileMap["lib/util.go"])
		assert.Equal(t, "# Test Project\n", fileMap["README.md"])
	})

	t.Run("FilePatterns", func(t *testing.T) {
		tmpDir := setupTestGitRepo(t, map[string]string{
			"main.go":      "package main",
			"main_test.go": "package main",
			"lib/util.go":  "package lib",
			"README.md":    "# Test",
		})
		defer os.RemoveAll(tmpDir)

		input := GitFileCollectorInput{
			ContextDir:      tmpDir,
			FilePatterns:    []string{"*.go", "lib/*.go"},
			ExcludePatterns: []string{"*_test.go"},
		}

		output, err := activity.Execute(nil, context.Background(), input)
		require.NoError(t, err)
		assert.Equal(t, 2, output.FileCount) // main.go and lib/util.go

		// Verify the correct files were collected
		filePaths := []string{}
		for _, f := range output.Files {
			filePaths = append(filePaths, f.Path)
		}
		assert.Contains(t, filePaths, "main.go")
		assert.Contains(t, filePaths, "lib/util.go")
		assert.NotContains(t, filePaths, "main_test.go")
		assert.NotContains(t, filePaths, "README.md")
	})

	t.Run("ExcludeBinary", func(t *testing.T) {
		tmpDir := setupTestGitRepo(t, map[string]string{
			"main.go":   "package main",
			"README.md": "# Test",
		})
		defer os.RemoveAll(tmpDir)

		// Add a binary file (with null bytes)
		binaryContent := []byte{0x00, 0x01, 0x02, 0x03}
		binaryPath := filepath.Join(tmpDir, "binary.dat")
		err := os.WriteFile(binaryPath, binaryContent, 0644)
		require.NoError(t, err)

		// Add an image file
		imagePath := filepath.Join(tmpDir, "image.png")
		err = os.WriteFile(imagePath, []byte("fake png content"), 0644)
		require.NoError(t, err)

		// Stage the new files
		cmd := exec.Command("git", "add", ".")
		cmd.Dir = tmpDir
		err = cmd.Run()
		require.NoError(t, err)

		input := GitFileCollectorInput{
			ContextDir:    tmpDir,
			IncludeStaged: true,
			ExcludeBinary: true,
		}

		output, err := activity.Execute(nil, context.Background(), input)
		require.NoError(t, err)

		// Should only have text files
		assert.Equal(t, 2, output.FileCount) // main.go and README.md

		// Check statistics
		assert.Equal(t, 2, output.Statistics.SkippedFiles)
		assert.Equal(t, 2, output.Statistics.SkippedReasons["binary_file"]) // binary.dat and image.png
	})

	t.Run("SizeLimits", func(t *testing.T) {
		largeContent := make([]byte, 1000)
		for i := range largeContent {
			largeContent[i] = 'a'
		}

		tmpDir := setupTestGitRepo(t, map[string]string{
			"small.txt": "small",
			"large.txt": string(largeContent),
		})
		defer os.RemoveAll(tmpDir)

		input := GitFileCollectorInput{
			ContextDir:  tmpDir,
			MaxFileSize: 100, // Only allow files up to 100 bytes
		}

		output, err := activity.Execute(nil, context.Background(), input)
		require.NoError(t, err)
		assert.Equal(t, 1, output.FileCount) // Only small.txt
		assert.Equal(t, 1, output.Statistics.SkippedFiles)
		assert.Equal(t, 1, output.Statistics.SkippedReasons["too_large"])
	})

	t.Run("UntrackedFiles", func(t *testing.T) {
		tmpDir := setupTestGitRepo(t, map[string]string{
			"tracked.go": "package main",
		})
		defer os.RemoveAll(tmpDir)

		// Add untracked file
		untrackedPath := filepath.Join(tmpDir, "untracked.go")
		err := os.WriteFile(untrackedPath, []byte("package untracked"), 0644)
		require.NoError(t, err)

		// Test without including untracked
		input := GitFileCollectorInput{
			ContextDir:       tmpDir,
			IncludeUntracked: false,
		}

		output, err := activity.Execute(nil, context.Background(), input)
		require.NoError(t, err)
		assert.Equal(t, 1, output.FileCount) // Only tracked.go

		// Test with including untracked
		input.IncludeUntracked = true
		output, err = activity.Execute(nil, context.Background(), input)
		require.NoError(t, err)
		assert.Equal(t, 2, output.FileCount) // tracked.go and untracked.go
	})

	t.Run("GitignoreRespect", func(t *testing.T) {
		tmpDir := setupTestGitRepo(t, map[string]string{
			"main.go":    "package main",
			".gitignore": "*.log\nbuild/\n",
		})
		defer os.RemoveAll(tmpDir)

		// Create ignored files
		err := os.WriteFile(filepath.Join(tmpDir, "debug.log"), []byte("log content"), 0644)
		require.NoError(t, err)

		err = os.MkdirAll(filepath.Join(tmpDir, "build"), 0755)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(tmpDir, "build", "output.bin"), []byte("binary"), 0644)
		require.NoError(t, err)

		// Create non-ignored untracked file
		err = os.WriteFile(filepath.Join(tmpDir, "new.go"), []byte("package new"), 0644)
		require.NoError(t, err)

		input := GitFileCollectorInput{
			ContextDir:       tmpDir,
			IncludeUntracked: true,
			UseGitignore:     true,
		}

		output, err := activity.Execute(nil, context.Background(), input)
		require.NoError(t, err)

		// Should have main.go, .gitignore, and new.go (but not debug.log or build/output.bin)
		assert.Equal(t, 3, output.FileCount)

		filePaths := []string{}
		for _, f := range output.Files {
			filePaths = append(filePaths, f.Path)
		}
		assert.Contains(t, filePaths, "main.go")
		assert.Contains(t, filePaths, ".gitignore")
		assert.Contains(t, filePaths, "new.go")
		assert.NotContains(t, filePaths, "debug.log")
		assert.NotContains(t, filePaths, "build/output.bin")
	})
}

func TestBinaryDetection(t *testing.T) {
	tests := []struct {
		name     string
		content  []byte
		path     string
		expected bool
	}{
		{
			name:     "Text file",
			content:  []byte("Hello, world!\n"),
			path:     "test.txt",
			expected: false,
		},
		{
			name:     "Go source file",
			content:  []byte("package main\n\nfunc main() {}\n"),
			path:     "main.go",
			expected: false,
		},
		{
			name:     "Binary by extension - PNG",
			content:  []byte("fake png"),
			path:     "image.png",
			expected: true,
		},
		{
			name:     "Binary by extension - JPEG",
			content:  []byte("fake jpeg"),
			path:     "photo.jpg",
			expected: true,
		},
		{
			name:     "Binary by extension - Executable",
			content:  []byte("fake exe"),
			path:     "program.exe",
			expected: true,
		},
		{
			name:     "Binary by content - null bytes",
			content:  []byte{0x00, 0x01, 0x02, 0x03},
			path:     "data.dat",
			expected: true,
		},
		{
			name:     "Binary by extension - Python compiled",
			content:  []byte("python bytecode"),
			path:     "module.pyc",
			expected: true,
		},
		{
			name:     "Binary by extension - Java class",
			content:  []byte("java bytecode"),
			path:     "Main.class",
			expected: true,
		},
		{
			name:     "Text with binary-like name",
			content:  []byte("This is text"),
			path:     "binary.txt",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isBinaryFile(tt.content, tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFileTypeDetection(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		expectedType string
		expectedMime string
	}{
		{
			name:         "Go file",
			path:         "main.go",
			expectedType: "code",
			expectedMime: "text/x-go",
		},
		{
			name:         "JavaScript file",
			path:         "app.js",
			expectedType: "code",
			expectedMime: "application/javascript",
		},
		{
			name:         "Markdown file",
			path:         "README.md",
			expectedType: "markdown",
			expectedMime: "text/markdown",
		},
		{
			name:         "JSON file",
			path:         "config.json",
			expectedType: "data",
			expectedMime: "application/json",
		},
		{
			name:         "YAML file",
			path:         "config.yaml",
			expectedType: "config",
			expectedMime: "application/x-yaml",
		},
		{
			name:         "Plain text file",
			path:         "notes.txt",
			expectedType: "txt",
			expectedMime: "text/plain",
		},
		{
			name:         "Unknown extension",
			path:         "file.xyz",
			expectedType: "txt",
			expectedMime: "text/plain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte("test content")
			fileType := detectFileType(content, tt.path)
			mimeType := detectMimeType(content, tt.path)

			assert.Equal(t, tt.expectedType, string(fileType))
			assert.Equal(t, tt.expectedMime, mimeType)
		})
	}
}
