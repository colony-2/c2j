// Package git provides Git repository operations for colony2.
package git

import (
	"context"
	"errors"
	"github.com/colony-2/c2j/pkg/git/internal/commands"
	"time"
)

// Status represents the current status of a Git repository.
type Status struct {
	Branch    string       `json:"branch"`
	Clean     bool         `json:"clean"`
	Files     []StatusFile `json:"files"`
	Ahead     int          `json:"ahead"`
	Behind    int          `json:"behind"`
	HasRemote bool         `json:"hasRemote"`
}

// StatusFile represents a single file in the Git status.
type StatusFile struct {
	Path   string `json:"path"`
	Status string `json:"status"` // "M", "A", "D", "?", etc.
}

// Commit represents a Git commit.
type Commit struct {
	Hash      string    `json:"hash"`
	Author    string    `json:"author"`
	Date      time.Time `json:"date"`
	Message   string    `json:"message"`
	ShortHash string    `json:"shortHash"`
}

// CloneOptions configures repository clone operations.
type CloneOptions struct {
	Branch         string                 // Specific branch to clone (empty = default branch)
	Depth          *int                   // Shallow clone depth (nil = full clone)
	SingleBranch   bool                   // Clone only the specified branch
	Bare           bool                   // Create a bare repository
	SparseCheckout *SparseCheckoutOptions // Sparse checkout config (nil = full checkout)
}

// SparseCheckoutOptions configures sparse checkout during clone operations.
type SparseCheckoutOptions struct {
	// Cone enables cone mode sparse checkout (recommended).
	// When true, Paths are treated as directory paths.
	// When false, sparse checkout is disabled.
	Cone bool

	// Paths specifies the directories to checkout in cone mode.
	// Example: []string{".c2/recipes", "docs"}
	// Empty slice disables sparse checkout.
	Paths []string
}

// FetchOptions configures fetch operations.
type FetchOptions struct {
	Remote string // Remote name (default: "origin")
	Branch string // Specific branch to fetch (empty = all branches)
	Prune  bool   // Remove remote-tracking references that no longer exist
	Tags   bool   // Fetch tags
}

// PullOptions configures pull operations.
type PullOptions struct {
	Remote      string // Remote name (default: "origin")
	Branch      string // Remote branch (empty = tracking branch)
	FastForward bool   // Only allow fast-forward merges
	Rebase      bool   // Rebase instead of merge
}

// PullResult contains the result of a pull operation.
type PullResult struct {
	Updated       bool     // Whether local branch was updated
	OldCommit     string   // Commit hash before pull
	NewCommit     string   // Commit hash after pull
	ConflictFiles []string // Files with conflicts (if any)
}

// PushOptions configures push operations.
type PushOptions struct {
	Remote      string // Remote name (default: "origin")
	Branch      string // Local branch to push (empty = current branch)
	Force       bool   // Force push (overwrite remote)
	SetUpstream bool   // Set upstream tracking branch
}

// PushResult contains the result of a push operation.
type PushResult struct {
	Pushed        bool // Whether any commits were pushed
	Rejected      bool // Whether push was rejected
	CommitsPushed int  // Number of commits pushed
}

// CheckoutOptions configures checkout operations.
type CheckoutOptions struct {
	CreateBranch string // Create new branch at ref (empty = no creation)
	Force        bool   // Discard local changes
	Detach       bool   // Detach HEAD (create detached HEAD state)
}

// InitOptions configures repository initialization.
type InitOptions struct {
	Bare          bool   // Create bare repository
	DefaultBranch string // Name of default branch (empty = "main")
}

// Remote represents a remote repository reference.
type Remote struct {
	Name     string
	FetchURL string
	PushURL  string
}

// FileInfo represents information about a file in a repository.
type FileInfo struct {
	Path  string
	IsDir bool
	Size  int64
}

// AuthType represents the type of authentication to use.
type AuthType int

const (
	// AuthTypeNone indicates no authentication.
	AuthTypeNone AuthType = iota
	// AuthTypeSSH indicates SSH key authentication.
	AuthTypeSSH
	// AuthTypeHTTPS indicates HTTPS authentication.
	AuthTypeHTTPS
)

// AuthConfig configures authentication for remote operations.
type AuthConfig struct {
	Type          AuthType
	SSHKeyPath    string // Path to SSH private key
	SSHPassword   string // SSH key passphrase
	HTTPSUsername string // HTTPS username
	HTTPSPassword string // HTTPS password/token
}

// GitError wraps git operation errors with additional context.
type GitError struct {
	Op     string // Operation that failed (e.g., "clone", "push")
	Path   string // Repository path
	Err    error  // Underlying error
	Stderr string // Git command stderr output (if applicable)
}

func (e *GitError) Error() string {
	if e.Stderr != "" {
		return "git " + e.Op + " at " + e.Path + ": " + e.Err.Error() + "\n" + e.Stderr
	}
	return "git " + e.Op + " at " + e.Path + ": " + e.Err.Error()
}

func (e *GitError) Unwrap() error {
	return e.Err
}

// Standard error variables.
var (
	ErrNotRepository      = errors.New("not a git repository")
	ErrNotFound           = errors.New("not found")
	ErrNotFastForward     = errors.New("not a fast-forward")
	ErrConflict           = errors.New("merge conflict")
	ErrAuthFailed         = errors.New("authentication failed")
	ErrUncommittedChanges = errors.New("uncommitted changes")
	ErrNetworkFailure     = errors.New("network failure")
)

// Repository provides Git operations for node directories.
type Repository interface {
	// GetStatus returns the current Git status of a node directory.
	GetStatus(ctx context.Context, nodePath string) (*Status, error)

	// GetDiff returns the diff of uncommitted changes.
	GetDiff(ctx context.Context, nodePath string, staged bool) (string, error)

	// GetHistory returns the commit history for a node.
	GetHistory(ctx context.Context, nodePath string, limit int) ([]Commit, error)

	// CreateCommit creates a new commit with the given message.
	CreateCommit(ctx context.Context, nodePath, message string) error

	// StageFiles stages the specified files for commit.
	StageFiles(ctx context.Context, nodePath string, files []string) error

	// UnstageFiles unstages the specified files.
	UnstageFiles(ctx context.Context, nodePath string, files []string) error

	// Clone creates a local copy of a remote repository.
	Clone(ctx context.Context, url string, localPath string, options CloneOptions) error

	// Fetch downloads objects and refs from remote repository.
	Fetch(ctx context.Context, nodePath string, options FetchOptions) error

	// Pull fetches from remote and integrates into current branch.
	Pull(ctx context.Context, nodePath string, options PullOptions) (*PullResult, error)

	// Push uploads local commits to remote repository.
	Push(ctx context.Context, nodePath string, options PushOptions) (*PushResult, error)

	// IsAncestor determines if one commit is an ancestor of another.
	IsAncestor(ctx context.Context, nodePath string, ancestor string, descendant string) (bool, error)

	// GetRemoteHead retrieves the commit hash of a remote branch.
	GetRemoteHead(ctx context.Context, nodePath string, remote string, branch string) (string, error)

	// GetCurrentCommit returns the commit hash of the current HEAD.
	GetCurrentCommit(ctx context.Context, nodePath string) (string, error)

	// Checkout switches to a different branch or commit.
	Checkout(ctx context.Context, nodePath string, ref string, options CheckoutOptions) error

	// InitRepository creates a new git repository.
	InitRepository(ctx context.Context, path string, options InitOptions) error

	// AddRemote adds a remote repository reference.
	AddRemote(ctx context.Context, nodePath string, name string, url string) error

	// ListRemotes returns all configured remotes.
	ListRemotes(ctx context.Context, nodePath string) ([]Remote, error)

	// GetFileAtCommit retrieves file content from a specific commit.
	GetFileAtCommit(ctx context.Context, nodePath string, commit string, filePath string) ([]byte, error)

	// ListFilesAtCommit lists all files in a directory at a specific commit.
	ListFilesAtCommit(ctx context.Context, nodePath string, commit string, dirPath string) ([]FileInfo, error)

	// IsRepoBare checks if a repository is bare (no working directory).
	IsRepoBare(ctx context.Context, repoPath string) (bool, error)

	// GetCurrentBranch returns the name of the current branch.
	// Returns error if repository is in detached HEAD state.
	GetCurrentBranch(ctx context.Context, repoPath string) (string, error)

	// ConfigureUser sets the user.name and user.email for a repository.
	// This is required before creating commits.
	ConfigureUser(ctx context.Context, repoPath string, name string, email string) error

	// UpdateRemoteURL updates the URL of an existing remote.
	UpdateRemoteURL(ctx context.Context, repoPath string, remoteName string, newURL string) error
}

// Config defines configuration for Git operations.
type Config struct {
	// DefaultAuthor is used when no Git user is configured.
	DefaultAuthor string

	// DefaultEmail is used when no Git email is configured.
	DefaultEmail string

	// Auth configures authentication for remote operations.
	Auth *AuthConfig
}

// NewRepository creates a new Git repository interface.
func NewRepository(config Config) Repository {
	repo := commands.New(config.DefaultAuthor, config.DefaultEmail)
	return &repoAdapter{repo: repo}
}

// repoAdapter adapts the internal repository to the public interface
type repoAdapter struct {
	repo *commands.Repository
}

func (a *repoAdapter) GetStatus(ctx context.Context, nodePath string) (*Status, error) {
	status, err := a.repo.GetStatus(ctx, nodePath)
	if err != nil {
		return nil, err
	}

	// Convert internal Status to public Status
	result := &Status{
		Branch:    status.Branch,
		Clean:     status.Clean,
		Files:     make([]StatusFile, len(status.Files)),
		Ahead:     status.Ahead,
		Behind:    status.Behind,
		HasRemote: status.HasRemote,
	}

	for i, f := range status.Files {
		result.Files[i] = StatusFile{
			Path:   f.Path,
			Status: f.Status,
		}
	}

	return result, nil
}

func (a *repoAdapter) GetDiff(ctx context.Context, nodePath string, staged bool) (string, error) {
	return a.repo.GetDiff(ctx, nodePath, staged)
}

func (a *repoAdapter) GetHistory(ctx context.Context, nodePath string, limit int) ([]Commit, error) {
	commits, err := a.repo.GetHistory(ctx, nodePath, limit)
	if err != nil {
		return nil, err
	}

	// Convert internal Commit to public Commit
	result := make([]Commit, len(commits))
	for i, c := range commits {
		result[i] = Commit{
			Hash:      c.Hash,
			Author:    c.Author,
			Date:      c.Date,
			Message:   c.Message,
			ShortHash: c.ShortHash,
		}
	}

	return result, nil
}

func (a *repoAdapter) CreateCommit(ctx context.Context, nodePath, message string) error {
	return a.repo.CreateCommit(ctx, nodePath, message)
}

func (a *repoAdapter) StageFiles(ctx context.Context, nodePath string, files []string) error {
	return a.repo.StageFiles(ctx, nodePath, files)
}

func (a *repoAdapter) UnstageFiles(ctx context.Context, nodePath string, files []string) error {
	return a.repo.UnstageFiles(ctx, nodePath, files)
}

func (a *repoAdapter) Clone(ctx context.Context, url string, localPath string, options CloneOptions) error {
	// Convert public SparseCheckoutOptions to internal type
	var internalSparseCheckout *commands.SparseCheckoutOptions
	if options.SparseCheckout != nil {
		internalSparseCheckout = &commands.SparseCheckoutOptions{
			Cone:  options.SparseCheckout.Cone,
			Paths: options.SparseCheckout.Paths,
		}
	}
	return a.repo.Clone(ctx, url, localPath, options.Branch, options.Depth, options.SingleBranch, options.Bare, internalSparseCheckout)
}

func (a *repoAdapter) Fetch(ctx context.Context, nodePath string, options FetchOptions) error {
	return a.repo.Fetch(ctx, nodePath, options.Remote, options.Branch, options.Prune, options.Tags)
}

func (a *repoAdapter) Pull(ctx context.Context, nodePath string, options PullOptions) (*PullResult, error) {
	updated, oldCommit, newCommit, conflictFiles, err := a.repo.Pull(ctx, nodePath, options.Remote, options.Branch, options.FastForward, options.Rebase)
	if err != nil {
		return nil, err
	}
	return &PullResult{
		Updated:       updated,
		OldCommit:     oldCommit,
		NewCommit:     newCommit,
		ConflictFiles: conflictFiles,
	}, nil
}

func (a *repoAdapter) Push(ctx context.Context, nodePath string, options PushOptions) (*PushResult, error) {
	pushed, rejected, commitsPushed, err := a.repo.Push(ctx, nodePath, options.Remote, options.Branch, options.Force, options.SetUpstream)
	if err != nil {
		return nil, err
	}
	return &PushResult{
		Pushed:        pushed,
		Rejected:      rejected,
		CommitsPushed: commitsPushed,
	}, nil
}

func (a *repoAdapter) IsAncestor(ctx context.Context, nodePath string, ancestor string, descendant string) (bool, error) {
	return a.repo.IsAncestor(ctx, nodePath, ancestor, descendant)
}

func (a *repoAdapter) GetRemoteHead(ctx context.Context, nodePath string, remote string, branch string) (string, error) {
	return a.repo.GetRemoteHead(ctx, nodePath, remote, branch)
}

func (a *repoAdapter) GetCurrentCommit(ctx context.Context, nodePath string) (string, error) {
	return a.repo.GetCurrentCommit(ctx, nodePath)
}

func (a *repoAdapter) Checkout(ctx context.Context, nodePath string, ref string, options CheckoutOptions) error {
	return a.repo.Checkout(ctx, nodePath, ref, options.CreateBranch, options.Force, options.Detach)
}

func (a *repoAdapter) InitRepository(ctx context.Context, path string, options InitOptions) error {
	return a.repo.InitRepository(ctx, path, options.Bare, options.DefaultBranch)
}

func (a *repoAdapter) AddRemote(ctx context.Context, nodePath string, name string, url string) error {
	return a.repo.AddRemote(ctx, nodePath, name, url)
}

func (a *repoAdapter) ListRemotes(ctx context.Context, nodePath string) ([]Remote, error) {
	remotes, err := a.repo.ListRemotes(ctx, nodePath)
	if err != nil {
		return nil, err
	}

	// Convert internal Remote to public Remote
	result := make([]Remote, len(remotes))
	for i, r := range remotes {
		result[i] = Remote{
			Name:     r.Name,
			FetchURL: r.FetchURL,
			PushURL:  r.PushURL,
		}
	}

	return result, nil
}

func (a *repoAdapter) GetFileAtCommit(ctx context.Context, nodePath string, commit string, filePath string) ([]byte, error) {
	return a.repo.GetFileAtCommit(ctx, nodePath, commit, filePath)
}

func (a *repoAdapter) ListFilesAtCommit(ctx context.Context, nodePath string, commit string, dirPath string) ([]FileInfo, error) {
	files, err := a.repo.ListFilesAtCommit(ctx, nodePath, commit, dirPath)
	if err != nil {
		return nil, err
	}

	// Convert internal FileInfo to public FileInfo
	result := make([]FileInfo, len(files))
	for i, f := range files {
		result[i] = FileInfo{
			Path:  f.Path,
			IsDir: f.IsDir,
			Size:  f.Size,
		}
	}

	return result, nil
}

func (a *repoAdapter) IsRepoBare(ctx context.Context, repoPath string) (bool, error) {
	return a.repo.IsRepoBare(ctx, repoPath)
}

func (a *repoAdapter) GetCurrentBranch(ctx context.Context, repoPath string) (string, error) {
	return a.repo.GetCurrentBranch(ctx, repoPath)
}

func (a *repoAdapter) ConfigureUser(ctx context.Context, repoPath string, name string, email string) error {
	return a.repo.ConfigureUser(ctx, repoPath, name, email)
}

func (a *repoAdapter) UpdateRemoteURL(ctx context.Context, repoPath string, remoteName string, newURL string) error {
	return a.repo.UpdateRemoteURL(ctx, repoPath, remoteName, newURL)
}
