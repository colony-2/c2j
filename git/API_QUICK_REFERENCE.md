# Git Package API Quick Reference

## Package Import

```go
import "github.com/colony-2/colony2/server/git/pkg/git"
```

## Creating a Repository Instance

```go
repo := git.NewRepository(git.Config{
    DefaultAuthor: "Your Name",
    DefaultEmail:  "your.email@example.com",
    Auth:          &git.AuthConfig{...}, // Optional
})
```

## Core Operations (Existing)

### Get Status
```go
status, err := repo.GetStatus(ctx, nodePath)
// status.Branch, status.Clean, status.Files, status.Ahead, status.Behind
```

### Get Diff
```go
diff, err := repo.GetDiff(ctx, nodePath, staged)
```

### Get History
```go
commits, err := repo.GetHistory(ctx, nodePath, limit)
```

### Stage Files
```go
err := repo.StageFiles(ctx, nodePath, []string{"file1.txt", "file2.txt"})
```

### Unstage Files
```go
err := repo.UnstageFiles(ctx, nodePath, []string{"file1.txt"})
```

### Create Commit
```go
err := repo.CreateCommit(ctx, nodePath, "Commit message")
```

## Remote Operations (New)

### Clone Repository
```go
err := repo.Clone(ctx, url, localPath, git.CloneOptions{
    Branch:       "main",           // Optional: specific branch
    Depth:        &depth,            // Optional: shallow clone depth
    SingleBranch: true,              // Optional: clone only one branch
    Bare:         false,             // Optional: bare repository
})
```

### Fetch from Remote
```go
err := repo.Fetch(ctx, nodePath, git.FetchOptions{
    Remote: "origin",    // Default: "origin"
    Branch: "main",      // Optional: specific branch
    Prune:  true,        // Remove deleted remote branches
    Tags:   true,        // Fetch tags
})
```

### Pull from Remote
```go
result, err := repo.Pull(ctx, nodePath, git.PullOptions{
    Remote:      "origin",    // Default: "origin"
    Branch:      "main",      // Optional: remote branch
    FastForward: true,        // Only fast-forward merges
    Rebase:      false,       // Use rebase instead of merge
})

// result.Updated, result.OldCommit, result.NewCommit, result.ConflictFiles
```

### Push to Remote
```go
result, err := repo.Push(ctx, nodePath, git.PushOptions{
    Remote:      "origin",    // Default: "origin"
    Branch:      "main",      // Optional: local branch
    Force:       false,       // Force push
    SetUpstream: true,        // Set upstream tracking
})

// result.Pushed, result.Rejected, result.CommitsPushed
```

## Repository Management (New)

### Initialize Repository
```go
err := repo.InitRepository(ctx, path, git.InitOptions{
    Bare:          false,     // Create bare repository
    DefaultBranch: "main",    // Default branch name
})
```

### Add Remote
```go
err := repo.AddRemote(ctx, nodePath, "origin", "https://github.com/user/repo.git")
```

### List Remotes
```go
remotes, err := repo.ListRemotes(ctx, nodePath)
for _, remote := range remotes {
    // remote.Name, remote.FetchURL, remote.PushURL
}
```

## Commit Operations (New)

### Get Current Commit
```go
commitHash, err := repo.GetCurrentCommit(ctx, nodePath)
// Returns full 40-character SHA-1 hash
```

### Get Remote HEAD
```go
commitHash, err := repo.GetRemoteHead(ctx, nodePath, "origin", "main")
// Returns remote branch HEAD without fetching
```

### Check Ancestry
```go
isAncestor, err := repo.IsAncestor(ctx, nodePath, ancestorHash, descendantHash)
// Returns true if ancestor is reachable from descendant
```

### Checkout Branch/Commit
```go
// Checkout existing branch
err := repo.Checkout(ctx, nodePath, "feature-branch", git.CheckoutOptions{})

// Create new branch
err := repo.Checkout(ctx, nodePath, "main", git.CheckoutOptions{
    CreateBranch: "new-feature",
})

// Checkout specific commit (detached HEAD)
err := repo.Checkout(ctx, nodePath, commitHash, git.CheckoutOptions{
    Detach: true,
})

// Force checkout (discard local changes)
err := repo.Checkout(ctx, nodePath, "main", git.CheckoutOptions{
    Force: true,
})
```

## File Operations (New)

### Get File at Commit
```go
content, err := repo.GetFileAtCommit(ctx, nodePath, commitHash, "path/to/file.txt")
// Returns []byte file content
```

### List Files at Commit
```go
files, err := repo.ListFilesAtCommit(ctx, nodePath, commitHash, "path/to/dir")
for _, file := range files {
    // file.Path, file.IsDir, file.Size
}
```

## Authentication (New Types)

```go
// SSH Authentication
auth := &git.AuthConfig{
    Type:        git.AuthTypeSSH,
    SSHKeyPath:  "/path/to/private/key",
    SSHPassword: "key-passphrase",
}

// HTTPS Authentication
auth := &git.AuthConfig{
    Type:          git.AuthTypeHTTPS,
    HTTPSUsername: "username",
    HTTPSPassword: "token-or-password",
}

// No Authentication
auth := &git.AuthConfig{
    Type: git.AuthTypeNone,
}
```

## Error Handling

```go
import "errors"

if err != nil {
    // Check for specific errors
    if errors.Is(err, git.ErrNotRepository) {
        // Not a git repository
    } else if errors.Is(err, git.ErrNotFastForward) {
        // Push/pull rejected
    } else if errors.Is(err, git.ErrConflict) {
        // Merge conflict
    } else if errors.Is(err, git.ErrAuthFailed) {
        // Authentication failed
    } else if errors.Is(err, git.ErrUncommittedChanges) {
        // Uncommitted changes prevent operation
    } else if errors.Is(err, git.ErrNetworkFailure) {
        // Network error
    } else if errors.Is(err, git.ErrNotFound) {
        // Branch, commit, or file not found
    }

    // Get detailed error information
    var gitErr *git.GitError
    if errors.As(err, &gitErr) {
        log.Printf("Operation: %s", gitErr.Op)
        log.Printf("Path: %s", gitErr.Path)
        log.Printf("Stderr: %s", gitErr.Stderr)
    }
}
```

## Common Patterns

### Check if Repository is Behind Remote
```go
// Fetch latest remote refs
err := repo.Fetch(ctx, nodePath, git.FetchOptions{Remote: "origin"})

// Get local and remote commits
localCommit, _ := repo.GetCurrentCommit(ctx, nodePath)
remoteCommit, _ := repo.GetRemoteHead(ctx, nodePath, "origin", "main")

// Check if local is behind
isBehind, _ := repo.IsAncestor(ctx, nodePath, localCommit, remoteCommit)
if isBehind && localCommit != remoteCommit {
    // Need to pull
    result, err := repo.Pull(ctx, nodePath, git.PullOptions{
        Remote:      "origin",
        FastForward: true,
    })
}
```

### Safe Push with Conflict Detection
```go
// Fetch first
err := repo.Fetch(ctx, nodePath, git.FetchOptions{Remote: "origin"})

// Check if we can fast-forward
localCommit, _ := repo.GetCurrentCommit(ctx, nodePath)
remoteCommit, _ := repo.GetRemoteHead(ctx, nodePath, "origin", "main")
canFF, _ := repo.IsAncestor(ctx, nodePath, remoteCommit, localCommit)

if !canFF {
    return errors.New("cannot push: not a fast-forward, pull first")
}

// Safe to push
result, err := repo.Push(ctx, nodePath, git.PushOptions{
    Remote: "origin",
    Branch: "main",
})
```

### Clone Specific Branch with Limited History
```go
depth := 1
err := repo.Clone(ctx, url, localPath, git.CloneOptions{
    Branch:       "main",
    Depth:        &depth,
    SingleBranch: true,
})
```

### Access Historical File Content
```go
// Get commits
commits, _ := repo.GetHistory(ctx, nodePath, 10)

// Access file at each commit
for _, commit := range commits {
    content, err := repo.GetFileAtCommit(ctx, nodePath, commit.Hash, "config.yaml")
    if err == nil {
        // Process historical content
    }
}
```

### Create Feature Branch Workflow
```go
// Ensure we're on main and up-to-date
repo.Checkout(ctx, nodePath, "main", git.CheckoutOptions{})
repo.Pull(ctx, nodePath, git.PullOptions{Remote: "origin", FastForward: true})

// Create feature branch
repo.Checkout(ctx, nodePath, "main", git.CheckoutOptions{
    CreateBranch: "feature/new-recipe",
})

// Make changes, commit, and push
repo.StageFiles(ctx, nodePath, []string{"recipe.yaml"})
repo.CreateCommit(ctx, nodePath, "Add new recipe")
repo.Push(ctx, nodePath, git.PushOptions{
    Remote:      "origin",
    SetUpstream: true,
})
```

## Complete Workflow Example

```go
package main

import (
    "context"
    "log"

    "github.com/colony-2/colony2/server/git/pkg/git"
)

func main() {
    ctx := context.Background()
    repo := git.NewRepository(git.Config{
        DefaultAuthor: "Recipe Bot",
        DefaultEmail:  "bot@example.com",
    })

    repoPath := "/path/to/recipes"
    remoteURL := "https://github.com/example/recipes.git"

    // Initialize and clone
    depth := 1
    err := repo.Clone(ctx, remoteURL, repoPath, git.CloneOptions{
        Branch:       "main",
        Depth:        &depth,
        SingleBranch: true,
    })
    if err != nil {
        log.Fatal(err)
    }

    // Make changes
    // ... edit files ...

    // Commit changes
    err = repo.StageFiles(ctx, repoPath, []string{"new-recipe.yaml"})
    if err != nil {
        log.Fatal(err)
    }

    err = repo.CreateCommit(ctx, repoPath, "Add new recipe")
    if err != nil {
        log.Fatal(err)
    }

    // Push to remote
    result, err := repo.Push(ctx, repoPath, git.PushOptions{
        Remote: "origin",
        Branch: "main",
    })
    if err != nil {
        log.Fatal(err)
    }

    if result.Pushed {
        log.Println("Successfully pushed changes!")
    }
}
```
