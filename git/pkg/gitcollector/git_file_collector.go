// Package gitcollector provides git-aware file collection for LLM consumption
package gitcollector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/core/pkg/file"
	"github.com/colony-2/c2j/git/internal/commands"
	"github.com/colony-2/c2j/git/pkg/common"
	"github.com/colony-2/c2j/recipe-core/pkg/ops"
)

// GitFileCollectorInput defines the input parameters
type GitFileCollectorInput struct {
	// Required
	ContextDir string `json:"context_dir" default:"{{ context.environment.worktree_path }}" validate:"required,dir"`

	// File filtering
	FilePatterns    []string `json:"file_patterns,omitempty" validate:"omitempty,dive,required"`
	ExcludePatterns []string `json:"exclude_patterns,omitempty" validate:"omitempty,dive,required"`

	// Size limits
	MaxFileSize  int `json:"max_file_size,omitempty" validate:"omitempty,gte=0"`
	MaxTotalSize int `json:"max_total_size,omitempty" validate:"omitempty,gte=0"`

	// Git options
	IncludeStaged    bool `json:"include_staged,omitempty"`
	IncludeUntracked bool `json:"include_untracked,omitempty"`
	UseGitignore     bool `json:"use_gitignore,omitempty"`
	ExcludeBinary    bool `json:"exclude_binary,omitempty"`

	// Processing options
	AutoDetectType  bool `json:"auto_detect_type,omitempty"`
	IncludeMetadata bool `json:"include_metadata,omitempty"`
}

// GitFileCollectorOutput defines the output structure
type GitFileCollectorOutput struct {
	Files      []file.File       `json:"files"`
	FileCount  int               `json:"file_count"`
	TotalSize  int64             `json:"total_size"`
	Repository GitRepositoryInfo `json:"repository"`
	Statistics FileStatistics    `json:"statistics,omitempty"`
}

// GitRepositoryInfo contains git repository metadata
type GitRepositoryInfo struct {
	Branch        string    `json:"branch"`
	CommitHash    string    `json:"commit_hash"`
	CommitMessage string    `json:"commit_message"`
	CommitTime    time.Time `json:"commit_time"`
	Author        string    `json:"author"`
	IsDirty       bool      `json:"is_dirty"`
	RemoteURL     string    `json:"remote_url,omitempty"`
}

// FileStatistics contains statistics about collected files
type FileStatistics struct {
	FilesByType      map[string]int `json:"files_by_type"`
	FilesByExtension map[string]int `json:"files_by_extension"`
	LargestFile      string         `json:"largest_file"`
	LargestFileSize  int64          `json:"largest_file_size"`
	SkippedFiles     int            `json:"skipped_files"`
	SkippedReasons   map[string]int `json:"skipped_reasons,omitempty"`
	TotalSize        int64          `json:"total_size"`
}

// gitFileCollectorActivity implements RegisterableOp
type gitFileCollectorActivity struct {
	gitRepo *commands.Repository
}

// NewGitFileCollectorActivity creates a new activity instance
func GetOp() ops.RegisterableOp {

	act := &gitFileCollectorActivity{gitRepo: commands.New("", "")}

	return ops.NewActivityMappedOpV2[GitFileCollectorInput, GitFileCollectorOutput](
		ops.OpMetadata{
			Type:        "git_file_collector",
			Description: "Collects files from a git repository with filtering and metadata",
			Version:     "1.0.0",
		},
		act.Execute)
}

// Execute runs the git file collection
func (a *gitFileCollectorActivity) Execute(
	_ ops.OpDependencies,
	ctx context.Context,
	input GitFileCollectorInput,
) (GitFileCollectorOutput, error) {
	// Apply defaults from config

	// Validate input
	if err := a.validateInput(input); err != nil {
		return GitFileCollectorOutput{}, err
	}

	// Validate git repository using common utils
	if err := common.ValidateRepository(input.ContextDir); err != nil {
		return GitFileCollectorOutput{}, fmt.Errorf("not a git repository: %w", err)
	}

	// Find the git root directory
	gitRoot, err := common.FindGitRoot(input.ContextDir)
	if err != nil {
		return GitFileCollectorOutput{}, fmt.Errorf("failed to find git root: %w", err)
	}

	// Get repository information using git commands.Repository from the git root
	status, err := a.gitRepo.GetStatus(ctx, gitRoot)
	if err != nil {
		return GitFileCollectorOutput{}, fmt.Errorf("failed to get repository status: %w", err)
	}

	// Get latest commit info from git root
	commits, err := a.gitRepo.GetHistory(ctx, gitRoot, 1)
	if err != nil || len(commits) == 0 {
		return GitFileCollectorOutput{}, fmt.Errorf("failed to get commit history: %w", err)
	}
	latestCommit := commits[0]

	// Build repository info from status and commit
	repoInfo := GitRepositoryInfo{
		Branch:        status.Branch,
		CommitHash:    latestCommit.Hash,
		CommitMessage: latestCommit.Message,
		CommitTime:    latestCommit.Date,
		Author:        latestCommit.Author,
		IsDirty:       !status.Clean,
		RemoteURL:     "", // Can be extracted if needed
	}

	// List files using git from the git root
	filePaths, err := a.listGitFiles(ctx, input, gitRoot)
	if err != nil {
		return GitFileCollectorOutput{}, fmt.Errorf("failed to list files: %w", err)
	}

	// Apply pattern filters
	filePaths = a.applyPatternFilters(filePaths, input)

	// Collect file contents from git root
	files, stats, err := a.collectFiles(ctx, gitRoot, filePaths, input)
	if err != nil {
		return GitFileCollectorOutput{}, fmt.Errorf("failed to collect files: %w", err)
	}

	return GitFileCollectorOutput{
		Files:      files,
		FileCount:  len(files),
		TotalSize:  stats.TotalSize,
		Repository: repoInfo,
		Statistics: stats,
	}, nil
}

// validateInput validates the input parameters
func (a *gitFileCollectorActivity) validateInput(input GitFileCollectorInput) error {
	// Check required fields
	if input.ContextDir == "" {
		return fmt.Errorf("context_dir is required")
	}

	// Check directory exists
	info, err := os.Stat(input.ContextDir)
	if err != nil {
		return fmt.Errorf("context directory not accessible: %w", err)
	}

	if !info.IsDir() {
		return fmt.Errorf("context_dir must be a directory")
	}

	// Validate patterns
	for _, pattern := range input.FilePatterns {
		if _, err := filepath.Match(pattern, "test"); err != nil {
			return fmt.Errorf("invalid file pattern %s: %w", pattern, err)
		}
	}

	for _, pattern := range input.ExcludePatterns {
		if _, err := filepath.Match(pattern, "test"); err != nil {
			return fmt.Errorf("invalid exclude pattern %s: %w", pattern, err)
		}
	}

	// Validate size limits
	if input.MaxFileSize < 0 {
		return fmt.Errorf("max_file_size must be non-negative")
	}

	if input.MaxTotalSize < 0 {
		return fmt.Errorf("max_total_size must be non-negative")
	}

	return nil
}

// listGitFiles lists files from the git repository
func (a *gitFileCollectorActivity) listGitFiles(ctx context.Context, input GitFileCollectorInput, gitRoot string) ([]string, error) {
	var files []string

	// Calculate relative path from git root to context directory
	// Resolve symlinks to avoid mismatches between gitRoot and ContextDir
	resolvedRoot, err := filepath.EvalSymlinks(gitRoot)
	if err != nil {
		resolvedRoot = gitRoot
	}
	resolvedContext, err := filepath.EvalSymlinks(input.ContextDir)
	if err != nil {
		resolvedContext = input.ContextDir
	}

	relPath, err := filepath.Rel(resolvedRoot, resolvedContext)
	if err != nil {
		return nil, fmt.Errorf("failed to get relative path: %w", err)
	}

	// If we're at the git root, relPath will be "."
	// If relPath escapes the root (starts with ".."), treat as root to avoid false negatives on symlinked mounts
	if relPath == "" {
		relPath = "."
	}
	restrictToSubdir := !(relPath == "." || strings.HasPrefix(relPath, ".."))

	// Get tracked files using common.ExecuteGitCommand from git root
	output, err := common.ExecuteGitCommand(ctx, gitRoot, "ls-files")
	if err != nil {
		return nil, fmt.Errorf("git ls-files failed: %w", err)
	}

	for _, line := range strings.Split(string(output), "\n") {
		if line != "" {
			// Filter files to those under the context directory if applicable
			if !restrictToSubdir || strings.HasPrefix(line, relPath+"/") {
				files = append(files, line)
			}
		}
	}

	// Get staged files if requested
	if input.IncludeStaged {
		output, err = common.ExecuteGitCommand(ctx, gitRoot, "diff", "--staged", "--name-only")
		if err == nil && len(output) > 0 {
			for _, line := range strings.Split(string(output), "\n") {
				if line != "" {
					// Filter to context directory if applicable
					if !restrictToSubdir || strings.HasPrefix(line, relPath+"/") {
						files = append(files, line)
					}
				}
			}
		}
	}

	// Get untracked files if requested
	if input.IncludeUntracked {
		args := []string{"ls-files", "--others"}
		if input.UseGitignore {
			args = append(args, "--exclude-standard")
		}

		output, err = common.ExecuteGitCommand(ctx, gitRoot, args...)
		if err == nil && len(output) > 0 {
			for _, line := range strings.Split(string(output), "\n") {
				if line != "" {
					// Filter to context directory if applicable
					if !restrictToSubdir || strings.HasPrefix(line, relPath+"/") {
						files = append(files, line)
					}
				}
			}
		}
	}

	return a.deduplicateFiles(files), nil
}

// deduplicateFiles removes duplicate file paths
func (a *gitFileCollectorActivity) deduplicateFiles(files []string) []string {
	seen := make(map[string]bool)
	result := []string{}

	for _, file := range files {
		if !seen[file] {
			seen[file] = true
			result = append(result, file)
		}
	}

	return result
}

// applyPatternFilters applies include/exclude patterns to file list
func (a *gitFileCollectorActivity) applyPatternFilters(files []string, input GitFileCollectorInput) []string {
	if len(input.FilePatterns) == 0 && len(input.ExcludePatterns) == 0 {
		return files
	}

	filtered := []string{}
	for _, file := range files {
		// Check include patterns
		if len(input.FilePatterns) > 0 {
			matched := false
			for _, pattern := range input.FilePatterns {
				if match, _ := filepath.Match(pattern, file); match {
					matched = true
					break
				}
				// Also check if pattern matches any part of the path
				if strings.Contains(pattern, "**") {
					// Simple glob matching for ** patterns
					simplifiedPattern := strings.ReplaceAll(pattern, "**", "*")
					if match, _ := filepath.Match(simplifiedPattern, file); match {
						matched = true
						break
					}
				}
			}
			if !matched {
				continue
			}
		}

		// Check exclude patterns
		excluded := false
		for _, pattern := range input.ExcludePatterns {
			if match, _ := filepath.Match(pattern, file); match {
				excluded = true
				break
			}
			// Also check if pattern matches any part of the path
			if strings.Contains(pattern, "**") {
				// Simple glob matching for ** patterns
				simplifiedPattern := strings.ReplaceAll(pattern, "**", "*")
				if match, _ := filepath.Match(simplifiedPattern, file); match {
					excluded = true
					break
				}
			}
		}

		if !excluded {
			filtered = append(filtered, file)
		}
	}

	return filtered
}

// collectFiles reads and processes files
func (a *gitFileCollectorActivity) collectFiles(
	ctx context.Context,
	baseDir string,
	filePaths []string,
	input GitFileCollectorInput,
) ([]file.File, FileStatistics, error) {
	var files []file.File
	var totalSize int64
	stats := FileStatistics{
		FilesByType:      make(map[string]int),
		FilesByExtension: make(map[string]int),
		SkippedReasons:   make(map[string]int),
	}

	for _, filePath := range filePaths {
		fullPath := filepath.Join(baseDir, filePath)

		// Check file size
		info, err := os.Stat(fullPath)
		if err != nil {
			stats.SkippedFiles++
			stats.SkippedReasons["not_found"]++
			continue
		}

		if input.MaxFileSize > 0 && info.Size() > int64(input.MaxFileSize) {
			stats.SkippedFiles++
			stats.SkippedReasons["too_large"]++
			continue
		}

		if input.MaxTotalSize > 0 && totalSize+info.Size() > int64(input.MaxTotalSize) {
			stats.SkippedFiles++
			stats.SkippedReasons["total_size_exceeded"]++
			continue
		}

		// Read file content
		content, err := os.ReadFile(fullPath)
		if err != nil {
			stats.SkippedFiles++
			stats.SkippedReasons["read_error"]++
			continue
		}

		// Check for binary files if exclusion is enabled
		if input.ExcludeBinary && isBinaryFile(content, filePath) {
			stats.SkippedFiles++
			stats.SkippedReasons["binary_file"]++
			continue
		}

		// Detect file type
		fileType := file.FileTypeText
		mimeType := "text/plain"
		if input.AutoDetectType {
			fileType = detectFileType(content, filePath)
			mimeType = detectMimeType(content, filePath)
		}

		// Create file object
		file := file.File{
			Path:     filePath,
			Name:     filepath.Base(filePath),
			Content:  content,
			MimeType: mimeType,
			Type:     fileType,
		}

		if input.IncludeMetadata {
			file.Metadata = map[string]interface{}{
				"size":        info.Size(),
				"modified":    info.ModTime(),
				"permissions": info.Mode().String(),
				"is_symlink":  info.Mode()&os.ModeSymlink != 0,
			}
		}

		files = append(files, file)
		totalSize += info.Size()

		// Update statistics
		stats.FilesByType[string(fileType)]++
		ext := filepath.Ext(filePath)
		if ext != "" {
			stats.FilesByExtension[ext]++
		}

		if info.Size() > stats.LargestFileSize {
			stats.LargestFile = filePath
			stats.LargestFileSize = info.Size()
		}
	}

	stats.TotalSize = totalSize
	return files, stats, nil
}

// isBinaryFile detects if a file is binary based on extension and content
func isBinaryFile(content []byte, path string) bool {
	// Check by extension first (similar to pre-commit config)
	binaryExtensions := []string{
		".gif", ".png", ".jpg", ".jpeg", ".ico", ".pdf",
		".zip", ".tar", ".gz", ".bz2", ".7z", ".rar",
		".exe", ".dll", ".so", ".dylib", ".a", ".o",
		".pyc", ".pyo", ".class", ".jar", ".war",
		".woff", ".woff2", ".ttf", ".otf", ".eot",
		".mp3", ".mp4", ".avi", ".mov", ".wmv",
		".db", ".sqlite", ".rdb",
	}

	ext := strings.ToLower(filepath.Ext(path))
	for _, binExt := range binaryExtensions {
		if ext == binExt {
			return true
		}
	}

	// Check for null bytes in first 8192 bytes (common heuristic)
	checkLen := len(content)
	if checkLen > 8192 {
		checkLen = 8192
	}

	for i := 0; i < checkLen; i++ {
		if content[i] == 0 {
			return true
		}
	}

	return false
}

// detectFileType detects the file type based on content and extension
func detectFileType(content []byte, path string) file.FileType {
	ext := strings.ToLower(filepath.Ext(path))

	// Check for common code file extensions
	codeExtensions := map[string]bool{
		".go": true, ".js": true, ".ts": true, ".py": true,
		".java": true, ".c": true, ".cpp": true, ".rs": true,
		".rb": true, ".php": true, ".swift": true, ".kt": true,
		".scala": true, ".r": true, ".m": true, ".h": true,
		".hpp": true, ".cs": true, ".vb": true, ".fs": true,
	}

	if codeExtensions[ext] {
		return file.FileTypeCode
	}

	// Check for markdown/document extensions
	if ext == ".md" {
		return file.FileTypeMarkdown
	}

	// Check for config extensions
	configExtensions := map[string]bool{
		".yaml": true, ".yml": true, ".toml": true, ".ini": true,
		".cfg": true, ".conf": true, ".properties": true, ".env": true,
	}

	if configExtensions[ext] {
		return file.FileTypeConfig
	}

	// Check for data extensions
	dataExtensions := map[string]bool{
		".json": true, ".xml": true, ".csv": true,
	}

	if dataExtensions[ext] {
		return file.FileTypeData
	}

	// Default to text
	return file.FileTypeText
}

// detectMimeType detects the MIME type based on content and extension
func detectMimeType(content []byte, path string) string {
	ext := strings.ToLower(filepath.Ext(path))

	mimeTypes := map[string]string{
		".go":   "text/x-go",
		".js":   "application/javascript",
		".ts":   "application/typescript",
		".py":   "text/x-python",
		".java": "text/x-java",
		".c":    "text/x-c",
		".cpp":  "text/x-c++",
		".rs":   "text/x-rust",
		".rb":   "text/x-ruby",
		".php":  "text/x-php",
		".json": "application/json",
		".xml":  "application/xml",
		".yaml": "application/x-yaml",
		".yml":  "application/x-yaml",
		".md":   "text/markdown",
		".txt":  "text/plain",
		".html": "text/html",
		".css":  "text/css",
	}

	if mimeType, ok := mimeTypes[ext]; ok {
		return mimeType
	}

	return "text/plain"
}
