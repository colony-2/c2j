package gitstate

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/git/pkg/common"
	"github.com/colony-2/c2j/git/pkg/gitcommit"
	"github.com/colony-2/c2j/git/pkg/gitshallow"
	"github.com/colony-2/swf-go/pkg/swf"
)

const ThinPackArtifactName = "__git_state_thin_pack__"

// Controller orchestrates cloning, restoring, and persisting git state per activity invocation.
type Controller struct {
	adapters map[string]StorageAdapter
}

// NewController constructs a Controller with the supplied adapters. If nil, a default file adapter is used.
func NewController(adapters map[string]StorageAdapter) *Controller {
	if adapters == nil {
		adapters = map[string]StorageAdapter{}
	}
	if _, ok := adapters["file"]; !ok {
		adapters["file"] = FileAdapter{}
	}
	return &Controller{
		adapters: adapters,
	}
}

// PrepareWorkspace ensures the worktree exists.
func (c *Controller) prepareWorkspace(ctx context.Context, task *GitTaskContext) error {
	if task == nil || task.GlobalGitTaskContext == nil {
		return fmt.Errorf("git workspace requires task context")
	}
	if task.GetWorktreePath() == "" {
		return fmt.Errorf("git workspace requires worktree path")
	}
	if task.GetBaseRepo() == "" {
		return fmt.Errorf("git workspace requires base repository path")
	}
	if task.GetBaseRef() == "" {
		return fmt.Errorf("git workspace requires base ref")
	}

	if err := c.cloneIfNeeded(ctx, task); err != nil {
		return err
	}

	if task.ResolvedBaseHash == "" {
		if head, err := common.GetCommitHash(ctx, task.GetWorktreePath(), "HEAD"); err == nil {
			task.ResolvedBaseHash = head
		}
	}

	return nil
}

// Restore replays thin packs when the target persist hash differs from the workspace state. It also prepares the workspace if it doesn't yet have a local copy of the repo.
func (c *Controller) Restore(ctx context.Context, task *GitTaskContext, thinPack swf.Artifact) error {
	err := c.prepareWorkspace(ctx, task)
	if err != nil {
		return err
	}

	if task.GetWorktreePath() == "" {
		return fmt.Errorf("git workspace requires worktree path")
	}

	targetRef := strings.TrimSpace(task.GetBaseRef())
	targetHash := strings.TrimSpace(task.GetPersistHash())

	if !dirExists(filepath.Join(task.GetWorktreePath(), ".git")) {
		return fmt.Errorf("git workspace missing .git directory: %s", task.GetWorktreePath())
	}

	if targetRef == "" && targetHash == "" {
		return nil
	}

	// Ref-first path: follow the ref tip and update resolved hash without thin packs.
	if targetHash == "" && targetRef != "" {
		hash, err := checkoutAndTrackRef(ctx, task.GetWorktreePath(), targetRef)
		if err != nil {
			return err
		}
		task.ResolvedBaseHash = hash
		task.ParentHash = ""
		task.PersistHash = ""
		return c.ensureCleanAfterRestore(ctx, task)
	}

	// if hashes are populated, operate in hash mode

	// LAZY OPTIMIZATION: Check if already at target before touching artifact
	current, err := common.GetCommitHash(ctx, task.GetWorktreePath(), "HEAD")
	if err != nil {
		return fmt.Errorf("determine current commit: %w", err)
	}
	if hashesEqual(current, targetHash) {
		task.ResolvedBaseHash = current
		return c.ensureCleanAfterRestore(ctx, task)
	}

	// We need to restore to a different commit
	rootHash := strings.TrimSpace(task.GetResolvedBaseHash())
	if rootHash == "" {
		rootHash = strings.TrimSpace(task.GetParentHash())
	}
	if rootHash == "" {
		rootHash = targetHash
	}
	if rootHash == "" {
		return fmt.Errorf("git workspace requires resolved base hash for restore")
	}

	// If we need to restore but have no thin pack artifact, return error
	if thinPack == nil {
		return fmt.Errorf("thin pack artifact required for restore to commit %s but none provided", targetHash)
	}

	// Create temp directory for this restore operation
	thinPackDir, err := os.MkdirTemp("", "thin-pack-restore-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(thinPackDir)

	// Extract artifact to temp directory with appropriate filename
	// Format: {commit_hash}-{parent_hash}-{root_hash}.pack
	expectedCommit := shortHash(task.GetPersistHash())
	expectedParent := shortHash(task.GetParentHash())
	expectedRoot := shortHash(rootHash)
	thinPackFilename := fmt.Sprintf("%s-%s-%s.pack", expectedCommit, expectedParent, expectedRoot)
	thinPackPath := filepath.Join(thinPackDir, thinPackFilename)

	if err := thinPack.SaveToFile(ctx, thinPackPath); err != nil {
		return fmt.Errorf("extract thin pack artifact: %w", err)
	}

	// Restore using thin pack
	restoreInput := gitcommit.RestoreCommitActivity{
		RepoPath:        task.GetWorktreePath(),
		TargetCommit:    task.GetPersistHash(),
		RootHash:        rootHash,
		StorageLocation: thinPackDir,
		Force:           true,
	}
	if _, err := gitcommit.RestoreCommit(ctx, restoreInput); err != nil {
		return fmt.Errorf("restore git state: %w", err)
	}

	if err := c.ensureCleanAfterRestore(ctx, task); err != nil {
		return err
	}
	task.ResolvedBaseHash = targetHash
	return nil
}

// Persist captures repository changes, writes thin packs, and returns the commit output and a thin pack artifact (or nil if no changes).
func (c *Controller) Persist(ctx context.Context, task *GitTaskContext) (*gitcommit.PersistCommitOutput, swf.Artifact, error) {
	scopePath, err := c.prepareScopedWorkspace(ctx, task)
	if err != nil {
		return nil, nil, err
	}

	// Create temp directory for thin pack persist operation
	thinPackDir, err := os.MkdirTemp("", "thin-pack-persist-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp dir: %w", err)
	}

	rootHash := task.GetResolvedBaseHash()
	if strings.TrimSpace(rootHash) == "" {
		var err error
		rootHash, err = common.GetCommitHash(ctx, task.GetWorktreePath(), "HEAD")
		if err != nil {
			os.RemoveAll(thinPackDir)
			return nil, nil, fmt.Errorf("resolve base hash: %w", err)
		}
		task.ResolvedBaseHash = rootHash
	}

	commitMessage := buildCommitMessage(task, "pending", "")
	author := task.GetGitAuthor()
	if author == "" && task.GetCellName() != "" {
		author = fmt.Sprintf("%s <%s@colony2>", task.GetCellName(), task.GetCellName())
	}

	persistInput := gitcommit.PersistCommitActivity{
		RepoPath:        task.GetWorktreePath(),
		StorageLocation: thinPackDir,
		RootHash:        rootHash,
		CommitMessage:   commitMessage,
		Author:          author,
	}

	output, err := gitcommit.PersistCommit(ctx, persistInput)
	if err != nil {
		os.RemoveAll(thinPackDir)
		return nil, nil, fmt.Errorf("persist commit failed %w", err)
	}

	if scopePath != "" {
		// Ensure the worktree remains clean after persistence.
		if err := common.EnsureCleanAfterRestore(ctx, task.GetWorktreePath(), scopePath, output.CommitHash); err != nil {
			os.RemoveAll(thinPackDir)
			return nil, nil, fmt.Errorf("failed to ensure clean repo after persist: %w", err)
		}
	}

	// Update task context with persist results
	if output.HasChanges {
		task.ParentHash = output.ParentHash
		task.PersistHash = output.CommitHash
		task.ResolvedBaseHash = output.CommitHash
	} else {
		task.ParentHash = output.ParentHash
		task.PersistHash = ""
		task.ResolvedBaseHash = rootHash
	}

	// If no changes or no thin pack written, clean up immediately and return output with nil artifact
	if !output.HasChanges || output.ThinPackPath == "" {
		os.RemoveAll(thinPackDir)
		return output, nil, nil
	}

	// Create lazy artifact with cleanup callback
	// The temp directory will be cleaned up by SWF after the artifact is consumed
	thinPackPath := output.ThinPackPath
	artifact := swf.NewArtifact(
		ThinPackArtifactName,
		func() (io.ReadCloser, int64, error) {
			f, err := os.Open(thinPackPath)
			if err != nil {
				return nil, 0, fmt.Errorf("open thin pack: %w", err)
			}
			info, err := f.Stat()
			if err != nil {
				f.Close()
				return nil, 0, fmt.Errorf("stat thin pack: %w", err)
			}
			return f, info.Size(), nil
		},
		func() error {
			return os.RemoveAll(thinPackDir)
		},
	)

	return output, artifact, nil
}

// PersistWithDiffs captures repository changes, writes thin packs and diffs, and returns the commit output with three artifacts:
// 1. Thin pack artifact (like Persist)
// 2. Diff from parent hash artifact
// 3. Diff from base hash artifact
func (c *Controller) PersistWithDiffs(ctx context.Context, task *GitTaskContext) (*gitcommit.PersistWithDiffsOutput, []swf.Artifact, error) {
	scopePath, err := c.prepareScopedWorkspace(ctx, task)
	if err != nil {
		return nil, nil, err
	}

	// Create temp directory for thin pack and diff persist operation
	persistDir, err := os.MkdirTemp("", "persist-with-diffs-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp dir: %w", err)
	}

	rootHash := task.GetResolvedBaseHash()
	if strings.TrimSpace(rootHash) == "" {
		var err error
		rootHash, err = common.GetCommitHash(ctx, task.GetWorktreePath(), "HEAD")
		if err != nil {
			os.RemoveAll(persistDir)
			return nil, nil, fmt.Errorf("resolve base hash: %w", err)
		}
		task.ResolvedBaseHash = rootHash
	}

	commitMessage := buildCommitMessage(task, "pending", "")
	author := task.GetGitAuthor()
	if author == "" && task.GetCellName() != "" {
		author = fmt.Sprintf("%s <%s@colony2>", task.GetCellName(), task.GetCellName())
	}

	persistInput := gitcommit.PersistCommitActivity{
		RepoPath:        task.GetWorktreePath(),
		StorageLocation: persistDir,
		RootHash:        rootHash,
		CommitMessage:   commitMessage,
		Author:          author,
	}

	// Call PersistCommitWithDiffs with baseHash from task context
	baseHash := task.GetResolvedBaseHash()
	output, err := gitcommit.PersistCommitWithDiffs(ctx, persistInput, baseHash)
	if err != nil {
		os.RemoveAll(persistDir)
		return nil, nil, fmt.Errorf("persist with diffs failed: %w", err)
	}

	if scopePath != "" {
		// Ensure the worktree remains clean after persistence.
		if err := common.EnsureCleanAfterRestore(ctx, task.GetWorktreePath(), scopePath, output.CommitHash); err != nil {
			os.RemoveAll(persistDir)
			return nil, nil, fmt.Errorf("failed to ensure clean repo after persist: %w", err)
		}
	}

	// Update task context with persist results
	if output.HasChanges {
		task.ParentHash = output.ParentHash
		task.PersistHash = output.CommitHash
		task.ResolvedBaseHash = output.CommitHash
	} else {
		task.ParentHash = output.ParentHash
		task.PersistHash = ""
		task.ResolvedBaseHash = rootHash
	}

	// If no changes, clean up and return empty artifacts
	if !output.HasChanges {
		os.RemoveAll(persistDir)
		return output, nil, nil
	}

	// Create artifacts for thin pack and diffs
	var artifacts []swf.Artifact

	// Determine which artifacts we'll create
	hasThinPack := output.ThinPackPath != ""
	hasDiffParent := output.DiffFromParentPath != ""
	hasDiffBase := output.DiffFromBasePath != ""

	// 1. Thin pack artifact (same as regular Persist)
	if hasThinPack {
		thinPackPath := output.ThinPackPath
		thinPackArtifact := swf.NewArtifact(
			ThinPackArtifactName,
			func() (io.ReadCloser, int64, error) {
				f, err := os.Open(thinPackPath)
				if err != nil {
					return nil, 0, fmt.Errorf("open thin pack: %w", err)
				}
				info, err := f.Stat()
				if err != nil {
					f.Close()
					return nil, 0, fmt.Errorf("stat thin pack: %w", err)
				}
				return f, info.Size(), nil
			},
			nil, // No cleanup yet
		)
		artifacts = append(artifacts, thinPackArtifact)
	}

	// 2. Diff from parent artifact
	if hasDiffParent {
		diffFromParentPath := output.DiffFromParentPath
		// Add cleanup callback if this is the last artifact
		var cleanup func() error
		if !hasDiffBase {
			cleanup = func() error { return os.RemoveAll(persistDir) }
		}
		diffFromParentArtifact := swf.NewArtifact(
			"diff_from_parent.diff",
			func() (io.ReadCloser, int64, error) {
				f, err := os.Open(diffFromParentPath)
				if err != nil {
					return nil, 0, fmt.Errorf("open diff from parent: %w", err)
				}
				info, err := f.Stat()
				if err != nil {
					f.Close()
					return nil, 0, fmt.Errorf("stat diff from parent: %w", err)
				}
				return f, info.Size(), nil
			},
			cleanup,
		)
		artifacts = append(artifacts, diffFromParentArtifact)
	}

	// 3. Diff from base artifact (always last if present)
	if hasDiffBase {
		diffFromBasePath := output.DiffFromBasePath
		diffFromBaseArtifact := swf.NewArtifact(
			"diff_from_base.diff",
			func() (io.ReadCloser, int64, error) {
				f, err := os.Open(diffFromBasePath)
				if err != nil {
					return nil, 0, fmt.Errorf("open diff from base: %w", err)
				}
				info, err := f.Stat()
				if err != nil {
					f.Close()
					return nil, 0, fmt.Errorf("stat diff from base: %w", err)
				}
				return f, info.Size(), nil
			},
			func() error { return os.RemoveAll(persistDir) }, // Cleanup on last artifact
		)
		artifacts = append(artifacts, diffFromBaseArtifact)
	}

	// If no artifacts were created but we still need to clean up
	if len(artifacts) == 0 {
		os.RemoveAll(persistDir)
	}

	return output, artifacts, nil
}

func (c *Controller) prepareScopedWorkspace(ctx context.Context, task *GitTaskContext) (string, error) {
	worktree := strings.TrimSpace(task.GetWorktreePath())
	if worktree == "" {
		return "", fmt.Errorf("git workspace requires worktree path")
	}

	scope, err := c.resolveScopePath(ctx, task)
	if err != nil {
		return "", err
	}

	if scope == "" {
		return "", nil
	}

	if _, err := common.PrepareScopedCommit(ctx, worktree, scope); err != nil {
		return "", err
	}

	return scope, nil
}

func (c *Controller) ensureCleanAfterRestore(ctx context.Context, task *GitTaskContext) error {
	worktree := strings.TrimSpace(task.GetWorktreePath())
	if worktree == "" {
		return fmt.Errorf("git workspace requires worktree path")
	}

	scope, err := c.resolveScopePath(ctx, task)
	if err != nil {
		return err
	}
	if scope == "" {
		scope = "."
	}

	return common.EnsureCleanAfterRestore(ctx, worktree, scope, task.GetPersistHash())
}

func (c *Controller) resolveScopePath(ctx context.Context, task *GitTaskContext) (string, error) {
	// Use CellPath for scope - it's required for proper scoping
	cellPath := strings.TrimSpace(task.GetCellPath())
	if cellPath == "" {
		return "", fmt.Errorf("cell_path is required for git persist operations")
	}

	// Check for absolute paths before trimming
	if filepath.IsAbs(cellPath) {
		return "", fmt.Errorf("invalid cell path %s", cellPath)
	}

	sanitized := filepath.ToSlash(strings.Trim(cellPath, "/"))
	if sanitized == "" {
		return "", fmt.Errorf("cell path cannot resolve to repository root")
	}
	if strings.Contains(sanitized, "..") {
		return "", fmt.Errorf("invalid cell path %s", cellPath)
	}
	return sanitized, nil
}

func (c *Controller) adapterFor(uri string) (StorageAdapter, error) {
	scheme := "file"
	if uri != "" {
		if parsed, err := url.Parse(uri); err == nil {
			if parsed.Scheme != "" {
				scheme = strings.ToLower(parsed.Scheme)
			}
		} else if idx := strings.Index(uri, ":"); idx > 0 {
			scheme = strings.ToLower(uri[:idx])
		}
	}
	adapter, ok := c.adapters[scheme]
	if !ok {
		return nil, fmt.Errorf("no storage adapter configured for scheme %s", scheme)
	}
	return adapter, nil
}

func checkoutAndTrackRef(ctx context.Context, repoPath, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("checkout requires ref")
	}

	if _, err := common.ExecuteGitCommand(ctx, repoPath, "fetch", "--all", "--tags", "--prune"); err != nil {
		return "", fmt.Errorf("fetch ref %s: %w", ref, err)
	}
	if _, err := common.ExecuteGitCommand(ctx, repoPath, "checkout", ref); err != nil {
		return "", fmt.Errorf("checkout ref %s: %w", ref, err)
	}

	hash, err := common.GetCommitHash(ctx, repoPath, "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve ref %s: %w", ref, err)
	}
	return hash, nil
}

func (c *Controller) cloneIfNeeded(ctx context.Context, task *GitTaskContext) error {
	if dirExists(filepath.Join(task.GetWorktreePath(), ".git")) {
		return nil
	}

	source := task.GetBaseRepo()
	if strings.HasPrefix(source, "file://") {
		path, err := fileURIToPath(source)
		if err != nil {
			return err
		}
		source = path
	}

	if err := os.MkdirAll(filepath.Dir(task.GetWorktreePath()), 0o755); err != nil {
		return fmt.Errorf("prepare worktree dir: %w", err)
	}

	input := gitshallow.GitShallowCloneInput{
		SourceDir:  source,
		TargetDir:  task.GetWorktreePath(),
		CommitHash: task.GetBaseRef(),
	}
	if _, err := gitshallow.GitShallowClone(ctx, input); err != nil {
		return fmt.Errorf("clone workspace: %w", err)
	}

	if head, err := common.GetCommitHash(ctx, task.GetWorktreePath(), "HEAD"); err == nil {
		task.ResolvedBaseHash = head
	}
	return nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func hashesEqual(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	if len(a) > 7 {
		a = a[:7]
	}
	if len(b) > 7 {
		b = b[:7]
	}
	return strings.EqualFold(a, b)
}

func shortHash(hash string) string {
	if len(hash) >= 7 {
		return hash[:7]
	}
	return hash
}
