# Git Module Documentation

## Overview

The Git module provides comprehensive Git repository operations for the colony2 system. It offers both direct repository management through the Repository interface and specialized activities for file collection, commit persistence, and shallow cloning operations that integrate with the recipe workflow system.

## Architecture

### Core Components

- **Repository Interface** (`pkg/git/repository.go`): Primary interface for Git operations including status, diff, history, commit, and file staging
- **Commands Implementation** (`internal/commands/commands.go`): Command-line Git wrapper providing concrete implementations
- **Activity System**: Registerable activities for workflow integration
  - **GitFileCollector** (`pkg/gitcollector/`): Git-aware file collection with filtering and metadata
  - **GitCommit Activities** (`pkg/gitcommit/`): Commit persistence and restoration using thin packs
  - **GitShallow** (`pkg/gitshallow/`): Shallow repository cloning operations
- **Common Utilities** (`pkg/common/`): Shared Git command execution, validation, and metadata parsing

### Key Relationships

```
Repository Interface
    ↓
Commands Implementation (git CLI wrapper)
    ↓
Common Utilities (validation, execution)

Activity System
    ↓
RegisterableOp Interface
    ↓
Recipe Worker Integration
```

## Key Interfaces

### Repository Interface

```go
type Repository interface {
    GetStatus(ctx context.Context, nodePath string) (*Status, error)
    GetDiff(ctx context.Context, nodePath string, staged bool) (string, error)
    GetHistory(ctx context.Context, nodePath string, limit int) ([]Commit, error)
    CreateCommit(ctx context.Context, nodePath, message string) error
    StageFiles(ctx context.Context, nodePath string, files []string) error
    UnstageFiles(ctx context.Context, nodePath string, files []string) error
}

func NewRepository(config Config) Repository
```

### Git File Collector Activity

```go
type GitFileCollectorInput struct {
    ContextDir       string   `json:"context_dir" validate:"required,dir"`
    FilePatterns     []string `json:"file_patterns,omitempty"`
    ExcludePatterns  []string `json:"exclude_patterns,omitempty"`
    MaxFileSize      int      `json:"max_file_size,omitempty"`
    MaxTotalSize     int      `json:"max_total_size,omitempty"`
    IncludeStaged    bool     `json:"include_staged,omitempty"`
    IncludeUntracked bool     `json:"include_untracked,omitempty"`
    UseGitignore     bool     `json:"use_gitignore,omitempty"`
    ExcludeBinary    bool     `json:"exclude_binary,omitempty"`
    AutoDetectType   bool     `json:"auto_detect_type,omitempty"`
    IncludeMetadata  bool     `json:"include_metadata,omitempty"`
}

// Registerable operation for recipe-core
func GetOp() ops.RegisterableOp {
    return ops.NewActivityMappedOpV2[GitFileCollectorInput, GitFileCollectorOutput](/* metadata */, /* handler */)
}

// Internal typed execution (for direct calls)
// use: activity := &gitcollector.gitFileCollectorActivity{}
//       output, err := activity.Execute(ops.Invocation{}, ctx, input)
```

### Commit Persistence Activities

```go
type PersistCommitInput struct {
    RepoPath        string        `json:"repo_path"`
    StorageLocation string        `json:"storage_location"`
    RootHash        string        `json:"root_hash"`
    CommitMessage   string        `json:"commit_message,omitempty"`
    Author          string        `json:"author,omitempty"`
    Timeout         time.Duration `json:"timeout,omitempty"`
}

type RestoreCommitInput struct {
    RepoPath        string        `json:"repo_path"`
    TargetCommit    string        `json:"target_commit"`
    RootHash        string        `json:"root_hash"`
    StorageLocation string        `json:"storage_location"`
    Force           bool          `json:"force,omitempty"`
    Timeout         time.Duration `json:"timeout,omitempty"`
}

// Registerable operations for recipe-core
func GetPersistOp() ops.RegisterableOp
func GetRestoreOp() ops.RegisterableOp

// Typed wrappers for direct calls
type PersistCommitActivityWrapper struct{}
// Execute(inv, ctx, input) (no separate config struct)
func (a *PersistCommitActivityWrapper) Execute(inv ops.Invocation, ctx context.Context, input PersistCommitInput) (PersistCommitOutput, error)

type RestoreCommitActivityWrapper struct{}
func NewRestoreCommitActivity() *RestoreCommitActivityWrapper
// Execute(inv, ctx, input) (no separate config struct)
func (a *RestoreCommitActivityWrapper) Execute(inv ops.Invocation, ctx context.Context, input RestoreCommitInput) (RestoreCommitOutput, error)
```

### Shallow Clone Activity

```go
type GitShallowInput struct {
    SourceDir  string `json:"source_dir"`
    TargetDir  string `json:"target_dir"`
    CommitHash string `json:"commit_hash"`
}

// Registerable operation for recipe-core
func GetOp() ops.RegisterableOp {
    return ops.NewActivityMappedOpV2[GitShallowInput, GitShallowOutput](/* metadata */, /* handler */)
}

// Typed wrapper for direct calls
type GitShallowActivityWrapper struct{}
func NewGitShallowActivity() *GitShallowActivityWrapper
// Execute(inv, ctx, input) (no separate config struct)
func (a *GitShallowActivityWrapper) Execute(inv ops.Invocation, ctx context.Context, input GitShallowInput) (GitShallowOutput, error)
```

### Common Utilities

```go
func ExecuteGitCommand(ctx context.Context, repoPath string, args ...string) ([]byte, error)
func ValidateRepository(repoPath string) error
func FindGitRoot(path string) (string, error)
func GetCommitHash(ctx context.Context, repoPath, ref string) (string, error)
func CommitExists(ctx context.Context, repoPath, commitHash string) bool
```

## Usage Examples

### Basic Repository Operations

```go
// Create repository instance
config := git.Config{
    DefaultAuthor: "Bot User",
    DefaultEmail:  "bot@example.com",
}
repo := git.NewRepository(config)

// Get repository status
status, err := repo.GetStatus(ctx, "/path/to/repo")
if err != nil {
    return err
}

// Stage and commit files
err = repo.StageFiles(ctx, "/path/to/repo", []string{"file1.go", "file2.go"})
if err != nil {
    return err
}

err = repo.CreateCommit(ctx, "/path/to/repo", "Add new features")
if err != nil {
    return err
}
```

### Git File Collection Activity

```yaml
# Recipe YAML
activities:
  - type: git_file_collector
    inputs:
      context_dir: "/workspace/project"
      file_patterns: ["*.go", "*.yaml"]
      exclude_patterns: ["*_test.go", "vendor/**"]
      max_file_size: 100000
      include_staged: true
      exclude_binary: true
      auto_detect_type: true
```

```go
// Registration for recipe-core
ops.Register(gitcollector.GetOp())

// Programmatic usage (direct typed execution within this module)
activity := &gitcollector.gitFileCollectorActivity{}
input := gitcollector.GitFileCollectorInput{
    ContextDir:      "/workspace/project",
    FilePatterns:    []string{"*.go", "*.yaml"},
    ExcludePatterns: []string{"*_test.go"},
    MaxFileSize:     100000,
    IncludeStaged:   true,
    ExcludeBinary:   true,
    AutoDetectType:  true,
}

output, err := activity.Execute(ctx, input)
```

### Commit Persistence Workflow

```yaml
# Persist current state
- type: git_persist_commit
  inputs:
    repo_path: "/workspace/project"
    storage_location: "/data/thin-packs"
    root_hash: "abc123def456"
    commit_message: "Checkpoint: feature implementation"

# Later restore to specific commit
- type: git_restore_commit
  inputs:
    repo_path: "/workspace/project"
    target_commit: "def456abc789"
    root_hash: "abc123def456"
    storage_location: "/data/thin-packs"
```

### Shallow Repository Clone

```go
// Registration for recipe-core
ops.Register(gitshallow.GetOp())

// Programmatic usage (typed)
activity := gitshallow.NewGitShallowActivity()
input := gitshallow.GitShallowInput{
    SourceDir:  "/original/repo",
    TargetDir:  "/workspace/clone",
    CommitHash: "abc123def456",
}

output, err := activity.Execute(ctx, input)
if err != nil {
    return err
}
// Repository cloned to output.ClonedPath
```

## Activity Registration

```go
// Register available git operations with the ops registry (no separate config types)
ops.Register(
    gitcollector.GetOp(),
    gitcommit.GetPersistOp(),
    gitcommit.GetRestoreOp(),
    gitshallow.GetOp(),
)
```
