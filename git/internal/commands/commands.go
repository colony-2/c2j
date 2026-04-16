package commands

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Status represents the current status of a Git repository
type Status struct {
	Branch       string       `json:"branch"`
	Clean        bool         `json:"clean"`
	Files        []StatusFile `json:"files"`
	Ahead        int          `json:"ahead"`
	Behind       int          `json:"behind"`
	HasRemote    bool         `json:"hasRemote"`
}

// StatusFile represents a single file in the Git status
type StatusFile struct {
	Path   string `json:"path"`
	Status string `json:"status"` // "M", "A", "D", "?", etc.
}

// Commit represents a Git commit
type Commit struct {
	Hash      string    `json:"hash"`
	Author    string    `json:"author"`
	Date      time.Time `json:"date"`
	Message   string    `json:"message"`
	ShortHash string    `json:"shortHash"`
}

// SparseCheckoutOptions configures sparse checkout during clone operations.
type SparseCheckoutOptions struct {
	Cone  bool
	Paths []string
}

// Repository implements git repository operations using command line git
type Repository struct {
	defaultAuthor string
	defaultEmail  string
}

// New creates a new git repository implementation
func New(defaultAuthor, defaultEmail string) *Repository {
	return &Repository{
		defaultAuthor: defaultAuthor,
		defaultEmail:  defaultEmail,
	}
}

// GetStatus returns the current Git status of a node directory
func (r *Repository) GetStatus(ctx context.Context, nodePath string) (*Status, error) {
	// Check if it's a git repository
	if !isGitRepo(nodePath) {
		return nil, fmt.Errorf("not a git repository")
	}

	status := &Status{
		Branch: "main",
		Clean:  true,
		Files:  []StatusFile{},
	}

	// Get current branch
	cmd := exec.CommandContext(ctx, "git", "branch", "--show-current")
	cmd.Dir = nodePath
	if output, err := cmd.Output(); err == nil {
		status.Branch = strings.TrimSpace(string(output))
	}

	// Get status
	cmd = exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = nodePath
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get git status: %w", err)
	}

	// Parse status output
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		status.Clean = false
		
		// Parse status line (format: "XY filename")
		if len(line) >= 3 {
			statusCode := string(line[0:2])
			filename := strings.TrimSpace(line[3:])
			
			// Convert to simple status
			simpleStatus := "?"
			if strings.Contains(statusCode, "M") {
				simpleStatus = "M"
			} else if strings.Contains(statusCode, "A") {
				simpleStatus = "A"
			} else if strings.Contains(statusCode, "D") {
				simpleStatus = "D"
			} else if strings.Contains(statusCode, "R") {
				simpleStatus = "R"
			} else if strings.Contains(statusCode, "C") {
				simpleStatus = "C"
			}
			
			status.Files = append(status.Files, StatusFile{
				Path:   filename,
				Status: simpleStatus,
			})
		}
	}

	// Check if we have a remote
	cmd = exec.CommandContext(ctx, "git", "remote")
	cmd.Dir = nodePath
	if output, err := cmd.Output(); err == nil && len(output) > 0 {
		status.HasRemote = true
		
		// Get ahead/behind info
		cmd = exec.CommandContext(ctx, "git", "rev-list", "--count", "--left-right", "@{upstream}...HEAD")
		cmd.Dir = nodePath
		if output, err := cmd.Output(); err == nil {
			parts := strings.Fields(string(output))
			if len(parts) == 2 {
				if behind, err := strconv.Atoi(parts[0]); err == nil {
					status.Behind = behind
				}
				if ahead, err := strconv.Atoi(parts[1]); err == nil {
					status.Ahead = ahead
				}
			}
		}
	}

	return status, nil
}

// GetDiff returns the diff of uncommitted changes
func (r *Repository) GetDiff(ctx context.Context, nodePath string, staged bool) (string, error) {
	if !isGitRepo(nodePath) {
		return "", fmt.Errorf("not a git repository")
	}

	args := []string{"diff"}
	if staged {
		args = append(args, "--cached")
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = nodePath
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get diff: %w", err)
	}

	return string(output), nil
}

// GetHistory returns the commit history for a node
func (r *Repository) GetHistory(ctx context.Context, nodePath string, limit int) ([]Commit, error) {
	if !isGitRepo(nodePath) {
		return nil, fmt.Errorf("not a git repository")
	}

	args := []string{"log", fmt.Sprintf("--max-count=%d", limit), "--pretty=format:%H|%an|%ae|%at|%s"}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = nodePath
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get history: %w", err)
	}

	var commits []Commit
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.Split(line, "|")
		if len(parts) < 5 {
			continue
		}

		timestamp, _ := strconv.ParseInt(parts[3], 10, 64)
		commit := Commit{
			Hash:      parts[0],
			Author:    fmt.Sprintf("%s <%s>", parts[1], parts[2]),
			Date:      time.Unix(timestamp, 0),
			Message:   parts[4],
			ShortHash: parts[0][:7],
		}
		commits = append(commits, commit)
	}

	return commits, nil
}

// CreateCommit creates a new commit with the given message
func (r *Repository) CreateCommit(ctx context.Context, nodePath, message string) error {
	if !isGitRepo(nodePath) {
		return fmt.Errorf("not a git repository")
	}

	// Check if there are changes to commit
	status, err := r.GetStatus(ctx, nodePath)
	if err != nil {
		return err
	}

	hasStaged := false
	for _, file := range status.Files {
		if file.Status == "A" || file.Status == "M" || file.Status == "D" {
			hasStaged = true
			break
		}
	}

	if !hasStaged {
		return fmt.Errorf("no changes to commit")
	}

	// Create commit
	cmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
	cmd.Dir = nodePath
	
	// Set author if configured
	if r.defaultAuthor != "" && r.defaultEmail != "" {
		cmd.Env = append(cmd.Environ(),
			fmt.Sprintf("GIT_AUTHOR_NAME=%s", r.defaultAuthor),
			fmt.Sprintf("GIT_AUTHOR_EMAIL=%s", r.defaultEmail),
			fmt.Sprintf("GIT_COMMITTER_NAME=%s", r.defaultAuthor),
			fmt.Sprintf("GIT_COMMITTER_EMAIL=%s", r.defaultEmail),
		)
	}

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create commit: %w\nOutput: %s", err, output)
	}

	return nil
}

// StageFiles stages the specified files for commit
func (r *Repository) StageFiles(ctx context.Context, nodePath string, files []string) error {
	if !isGitRepo(nodePath) {
		return fmt.Errorf("not a git repository")
	}

	if len(files) == 0 {
		return nil
	}

	args := append([]string{"add"}, files...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = nodePath
	
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to stage files: %w\nOutput: %s", err, output)
	}

	return nil
}

// UnstageFiles unstages the specified files
func (r *Repository) UnstageFiles(ctx context.Context, nodePath string, files []string) error {
	if !isGitRepo(nodePath) {
		return fmt.Errorf("not a git repository")
	}

	if len(files) == 0 {
		return nil
	}

	args := append([]string{"reset", "HEAD"}, files...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = nodePath
	
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to unstage files: %w\nOutput: %s", err, output)
	}

	return nil
}

// Clone creates a local copy of a remote repository
func (r *Repository) Clone(ctx context.Context, url string, localPath string, branch string, depth *int, singleBranch bool, bare bool, sparseCheckout *SparseCheckoutOptions) error {
	// Build clone arguments
	args := []string{"clone"}

	if branch != "" {
		args = append(args, "--branch", branch)
	}

	if depth != nil {
		args = append(args, "--depth", fmt.Sprintf("%d", *depth))
	}

	if singleBranch {
		args = append(args, "--single-branch")
	}

	if bare {
		args = append(args, "--bare")
	}

	// Sparse checkout requires --no-checkout to avoid initial full checkout
	if sparseCheckout != nil && sparseCheckout.Cone && len(sparseCheckout.Paths) > 0 {
		args = append(args, "--no-checkout")
	}

	args = append(args, url, localPath)

	// Execute clone
	cmd := exec.CommandContext(ctx, "git", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to clone repository: %w\nOutput: %s", err, output)
	}

	// Configure sparse checkout if requested
	if sparseCheckout != nil && sparseCheckout.Cone && len(sparseCheckout.Paths) > 0 {
		if err := r.configureSparseCheckout(ctx, localPath, sparseCheckout.Paths); err != nil {
			return fmt.Errorf("failed to configure sparse checkout: %w", err)
		}
	}

	return nil
}

// configureSparseCheckout initializes and configures sparse checkout in cone mode.
// This must be called after clone with --no-checkout flag.
func (r *Repository) configureSparseCheckout(ctx context.Context, repoPath string, paths []string) error {
	// Initialize sparse checkout in cone mode
	cmd := exec.CommandContext(ctx, "git", "sparse-checkout", "init", "--cone")
	cmd.Dir = repoPath
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to init sparse checkout: %w\nOutput: %s", err, output)
	}

	// Set sparse checkout paths
	args := append([]string{"sparse-checkout", "set"}, paths...)
	cmd = exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to set sparse checkout paths: %w\nOutput: %s", err, output)
	}

	// Checkout the files (this populates working directory with sparse paths)
	cmd = exec.CommandContext(ctx, "git", "checkout")
	cmd.Dir = repoPath
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to checkout sparse paths: %w\nOutput: %s", err, output)
	}

	return nil
}

// Fetch downloads objects and refs from remote repository
func (r *Repository) Fetch(ctx context.Context, nodePath string, remote string, branch string, prune bool, tags bool) error {
	if !isGitRepo(nodePath) {
		return fmt.Errorf("not a git repository")
	}

	args := []string{"fetch"}

	if remote == "" {
		remote = "origin"
	}
	args = append(args, remote)

	if branch != "" {
		args = append(args, branch)
	}

	if prune {
		args = append(args, "--prune")
	}

	if tags {
		args = append(args, "--tags")
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = nodePath

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to fetch: %w\nOutput: %s", err, output)
	}

	return nil
}

// Pull fetches from remote and integrates into current branch
func (r *Repository) Pull(ctx context.Context, nodePath string, remote string, branch string, fastForward bool, rebase bool) (updated bool, oldCommit string, newCommit string, conflictFiles []string, err error) {
	if !isGitRepo(nodePath) {
		return false, "", "", nil, fmt.Errorf("not a git repository")
	}

	// Get current commit
	oldCommit, err = r.GetCurrentCommit(ctx, nodePath)
	if err != nil {
		return false, "", "", nil, err
	}

	args := []string{"pull"}

	if remote == "" {
		remote = "origin"
	}
	args = append(args, remote)

	if branch != "" {
		args = append(args, branch)
	}

	if fastForward {
		args = append(args, "--ff-only")
	}

	if rebase {
		args = append(args, "--rebase")
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = nodePath

	output, pullErr := cmd.CombinedOutput()
	outputStr := string(output)

	// Get new commit
	newCommit, _ = r.GetCurrentCommit(ctx, nodePath)

	// Check for conflicts
	if pullErr != nil {
		if strings.Contains(outputStr, "CONFLICT") {
			// Parse conflict files
			cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "--diff-filter=U")
			cmd.Dir = nodePath
			if conflictOutput, err := cmd.Output(); err == nil {
				lines := strings.Split(string(conflictOutput), "\n")
				for _, line := range lines {
					if line != "" {
						conflictFiles = append(conflictFiles, line)
					}
				}
			}
			return false, oldCommit, newCommit, conflictFiles, fmt.Errorf("merge conflict")
		}
		return false, oldCommit, newCommit, nil, fmt.Errorf("failed to pull: %w\nOutput: %s", pullErr, output)
	}

	updated = oldCommit != newCommit
	return updated, oldCommit, newCommit, nil, nil
}

// Push uploads local commits to remote repository
func (r *Repository) Push(ctx context.Context, nodePath string, remote string, branch string, force bool, setUpstream bool) (pushed bool, rejected bool, commitsPushed int, err error) {
	if !isGitRepo(nodePath) {
		return false, false, 0, fmt.Errorf("not a git repository")
	}

	if remote == "" {
		remote = "origin"
	}

	// Get the remote URL to determine if we need special handling
	remotes, err := r.ListRemotes(ctx, nodePath)
	if err != nil {
		return false, false, 0, fmt.Errorf("failed to list remotes: %w", err)
	}

	var originURL string
	for _, rem := range remotes {
		if rem.Name == remote {
			originURL = rem.FetchURL
			break
		}
	}

	if originURL != "" {
		// Check if origin is a local path (not a remote URL like git@github.com)
		// Only local paths can be checked for bare status
		isLocalPath := !strings.Contains(originURL, "://") && !strings.HasPrefix(originURL, "git@")

		if isLocalPath {
			// Check if origin is a bare repository
			isBare, err := r.IsRepoBare(ctx, originURL)
			if err != nil {
				// If we can't determine, assume it's safe to push normally
				isBare = true
			}

			// Configure origin for non-bare repository if needed
			if !isBare {
				// Ensure we have the branch name
				if branch == "" {
					branch, err = r.GetCurrentBranch(ctx, nodePath)
					if err != nil {
						return false, false, 0, fmt.Errorf("failed to get workspace branch: %w", err)
					}
				}

				// Get origin's current branch
				originBranch, err := r.GetCurrentBranch(ctx, originURL)
				if err != nil {
					// Can't get branch - possibly detached HEAD or other issue
					// Safer to not configure updateInstead
				} else {
					// If pushing to currently checked-out branch, configure updateInstead
					if branch == originBranch {
						cmd := exec.CommandContext(ctx, "git", "config", "receive.denyCurrentBranch", "updateInstead")
						cmd.Dir = originURL
						if err := cmd.Run(); err != nil {
							return false, false, 0, fmt.Errorf("failed to configure receive.denyCurrentBranch: %w", err)
						}
					}
					// If different branch, no special config needed (safe to push)
				}
			}
		}
		// If remote URL, no special config needed (handled by remote server)
	}

	// Ensure we have a branch when setUpstream is true or pushing to local repo
	// This handles git configs with push.default=nothing
	if branch == "" && (setUpstream || (originURL != "" && !strings.Contains(originURL, "://") && !strings.HasPrefix(originURL, "git@"))) {
		branch, err = r.GetCurrentBranch(ctx, nodePath)
		if err != nil {
			return false, false, 0, fmt.Errorf("failed to get current branch: %w", err)
		}
	}

	args := []string{"push"}

	if force {
		args = append(args, "--force")
	}

	if setUpstream {
		args = append(args, "--set-upstream")
	}

	args = append(args, remote)

	if branch != "" {
		args = append(args, branch)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = nodePath

	output, pushErr := cmd.CombinedOutput()
	outputStr := string(output)

	if pushErr != nil {
		if strings.Contains(outputStr, "rejected") || strings.Contains(outputStr, "non-fast-forward") {
			return false, true, 0, fmt.Errorf("push rejected: not a fast-forward")
		}
		return false, false, 0, fmt.Errorf("failed to push: %w\nOutput: %s", pushErr, output)
	}

	// Check if anything was pushed
	if strings.Contains(outputStr, "Everything up-to-date") {
		return false, false, 0, nil
	}

	return true, false, 0, nil
}

// IsAncestor determines if one commit is an ancestor of another
func (r *Repository) IsAncestor(ctx context.Context, nodePath string, ancestor string, descendant string) (bool, error) {
	if !isGitRepo(nodePath) {
		return false, fmt.Errorf("not a git repository")
	}

	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = nodePath

	err := cmd.Run()
	if err == nil {
		return true, nil
	}

	// Exit code 1 means not an ancestor, other codes are errors
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() == 1 {
			return false, nil
		}
	}

	return false, fmt.Errorf("failed to check ancestry: %w", err)
}

// GetRemoteHead retrieves the commit hash of a remote branch
func (r *Repository) GetRemoteHead(ctx context.Context, nodePath string, remote string, branch string) (string, error) {
	if !isGitRepo(nodePath) {
		return "", fmt.Errorf("not a git repository")
	}

	if remote == "" {
		remote = "origin"
	}

	cmd := exec.CommandContext(ctx, "git", "ls-remote", remote, branch)
	cmd.Dir = nodePath

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get remote head: %w", err)
	}

	// Parse output (format: "hash\tref")
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 1 {
			return parts[0], nil
		}
	}

	return "", fmt.Errorf("branch not found on remote")
}

// GetCurrentCommit returns the commit hash of the current HEAD
func (r *Repository) GetCurrentCommit(ctx context.Context, nodePath string) (string, error) {
	if !isGitRepo(nodePath) {
		return "", fmt.Errorf("not a git repository")
	}

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = nodePath

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current commit: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// Checkout switches to a different branch or commit
func (r *Repository) Checkout(ctx context.Context, nodePath string, ref string, createBranch string, force bool, detach bool) error {
	if !isGitRepo(nodePath) {
		return fmt.Errorf("not a git repository")
	}

	args := []string{"checkout"}

	if force {
		args = append(args, "--force")
	}

	if detach {
		args = append(args, "--detach")
	}

	if createBranch != "" {
		args = append(args, "-b", createBranch)
	}

	args = append(args, ref)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = nodePath

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to checkout: %w\nOutput: %s", err, output)
	}

	return nil
}

// InitRepository creates a new git repository
func (r *Repository) InitRepository(ctx context.Context, path string, bare bool, defaultBranch string) error {
	args := []string{"init"}

	if bare {
		args = append(args, "--bare")
	}

	if defaultBranch != "" {
		args = append(args, "--initial-branch", defaultBranch)
	} else {
		args = append(args, "--initial-branch", "main")
	}

	args = append(args, path)

	cmd := exec.CommandContext(ctx, "git", args...)

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to init repository: %w\nOutput: %s", err, output)
	}

	return nil
}

// AddRemote adds a remote repository reference
func (r *Repository) AddRemote(ctx context.Context, nodePath string, name string, url string) error {
	if !isGitRepo(nodePath) {
		return fmt.Errorf("not a git repository")
	}

	cmd := exec.CommandContext(ctx, "git", "remote", "add", name, url)
	cmd.Dir = nodePath

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add remote: %w\nOutput: %s", err, output)
	}

	return nil
}

// Remote represents a remote repository reference
type Remote struct {
	Name     string
	FetchURL string
	PushURL  string
}

// ListRemotes returns all configured remotes
func (r *Repository) ListRemotes(ctx context.Context, nodePath string) ([]Remote, error) {
	if !isGitRepo(nodePath) {
		return nil, fmt.Errorf("not a git repository")
	}

	// Get list of remote names
	cmd := exec.CommandContext(ctx, "git", "remote")
	cmd.Dir = nodePath

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list remotes: %w", err)
	}

	remotes := make([]Remote, 0)
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, name := range lines {
		if name == "" {
			continue
		}

		remote := Remote{Name: name}

		// Get the actual configured URL without git's insteadOf rewrites
		// Using git config --get instead of git remote -v
		urlCmd := exec.CommandContext(ctx, "git", "config", "--get", "remote."+name+".url")
		urlCmd.Dir = nodePath
		urlOutput, err := urlCmd.Output()
		if err == nil {
			url := strings.TrimSpace(string(urlOutput))
			remote.FetchURL = url
			remote.PushURL = url
		}

		// Check if there's a separate pushurl configured
		pushURLCmd := exec.CommandContext(ctx, "git", "config", "--get", "remote."+name+".pushurl")
		pushURLCmd.Dir = nodePath
		pushURLOutput, err := pushURLCmd.Output()
		if err == nil {
			remote.PushURL = strings.TrimSpace(string(pushURLOutput))
		}

		remotes = append(remotes, remote)
	}

	return remotes, nil
}

// GetFileAtCommit retrieves file content from a specific commit
func (r *Repository) GetFileAtCommit(ctx context.Context, nodePath string, commit string, filePath string) ([]byte, error) {
	if !isGitRepo(nodePath) {
		return nil, fmt.Errorf("not a git repository")
	}

	cmd := exec.CommandContext(ctx, "git", "show", fmt.Sprintf("%s:%s", commit, filePath))
	cmd.Dir = nodePath

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get file at commit: %w", err)
	}

	return output, nil
}

// FileInfo represents information about a file in a repository
type FileInfo struct {
	Path  string
	IsDir bool
	Size  int64
}

// ListFilesAtCommit lists all files in a directory at a specific commit
func (r *Repository) ListFilesAtCommit(ctx context.Context, nodePath string, commit string, dirPath string) ([]FileInfo, error) {
	if !isGitRepo(nodePath) {
		return nil, fmt.Errorf("not a git repository")
	}

	// Use git ls-tree to list files
	args := []string{"ls-tree", "-l", commit}
	if dirPath != "" {
		args = append(args, dirPath)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = nodePath

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list files at commit: %w", err)
	}

	var files []FileInfo
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		// Format: "mode type hash size\tname"
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}

		mode := parts[0]
		objType := parts[1]
		size := int64(0)

		// Parse size (might be "-" for directories)
		if parts[3] != "-" {
			if s, err := strconv.ParseInt(parts[3], 10, 64); err == nil {
				size = s
			}
		}

		// Get name (after tab)
		tabIndex := strings.Index(line, "\t")
		if tabIndex == -1 {
			continue
		}
		name := line[tabIndex+1:]

		files = append(files, FileInfo{
			Path:  name,
			IsDir: objType == "tree" || mode == "040000",
			Size:  size,
		})
	}

	return files, nil
}

// IsRepoBare checks if a repository is bare (no working directory).
func (r *Repository) IsRepoBare(ctx context.Context, repoPath string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-bare-repository")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to check if repo is bare: %w", err)
	}
	return strings.TrimSpace(string(output)) == "true", nil
}

// GetCurrentBranch returns the name of the current branch.
// Returns error if repository is in detached HEAD state.
func (r *Repository) GetCurrentBranch(ctx context.Context, repoPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	branch := strings.TrimSpace(string(output))
	if branch == "HEAD" {
		// Detached HEAD state
		return "", fmt.Errorf("repository is in detached HEAD state")
	}
	return branch, nil
}

// ConfigureUser sets the user.name and user.email for a repository.
// This is required before creating commits.
func (r *Repository) ConfigureUser(ctx context.Context, repoPath string, name string, email string) error {
	// Set user.name
	cmd := exec.CommandContext(ctx, "git", "config", "user.name", name)
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set user.name: %w", err)
	}

	// Set user.email
	cmd = exec.CommandContext(ctx, "git", "config", "user.email", email)
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set user.email: %w", err)
	}

	return nil
}

// UpdateRemoteURL updates the URL of an existing remote.
func (r *Repository) UpdateRemoteURL(ctx context.Context, repoPath string, remoteName string, newURL string) error {
	cmd := exec.CommandContext(ctx, "git", "remote", "set-url", remoteName, newURL)
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to update remote URL: %w", err)
	}
	return nil
}

// isGitRepo checks if a directory is a git repository
func isGitRepo(dirPath string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = dirPath

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	return cmd.Run() == nil
}