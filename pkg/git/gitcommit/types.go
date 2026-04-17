package gitcommit

import (
	"time"
)

// PersistCommitActivity defines the recipe operation for persisting Git commits
type PersistCommitActivity struct {
	// Required inputs
	RepoPath        string `json:"repo_path"`        // Path to the local Git repository
	StorageLocation string `json:"storage_location"` // Directory path where thin packs will be stored
	RootHash        string `json:"root_hash"`        // Base commit hash this set was built upon

	// Optional configuration
	CommitMessage string        `json:"commit_message,omitempty"` // Message for the commit
	Author        string        `json:"author,omitempty"`         // Author name and email
	Timeout       time.Duration `json:"timeout,omitempty"`        // Operation timeout
}

// PersistCommitOutput represents the output from persist operation
type PersistCommitOutput struct {
	CommitHash   string    `json:"commit_hash"`    // SHA-1 hash of created commit
	ParentHash   string    `json:"parent_hash"`    // SHA-1 hash of parent commit
	ThinPackPath string    `json:"thin_pack_path"` // Full path to generated thin pack
	ThinPackSize int64     `json:"thin_pack_size"` // Size of thin pack in bytes
	CreatedAt    time.Time `json:"created_at"`     // Timestamp of operation
	HasChanges   bool      `json:"has_changes"`    // True when a new commit was created
}

// RestoreCommitActivity defines the recipe operation for restoring Git commits
type RestoreCommitActivity struct {
	// Required inputs
	RepoPath        string `json:"repo_path"`        // Path to the local Git repository
	TargetCommit    string `json:"target_commit"`    // Commit hash to restore to
	RootHash        string `json:"root_hash"`        // Root commit hash for this set
	StorageLocation string `json:"storage_location"` // Directory containing thin packs

	// Optional configuration
	Force   bool          `json:"force,omitempty"`   // Force checkout even with uncommitted changes
	Timeout time.Duration `json:"timeout,omitempty"` // Operation timeout
}

// RestoreCommitOutput represents the output from restore operation
type RestoreCommitOutput struct {
	Success          bool      `json:"success"`                      // Whether restore succeeded
	CurrentCommit    string    `json:"current_commit"`               // Current commit hash after restore
	RestoredFrom     string    `json:"restored_from"`                // Source of restore: "repository" or "thin_packs"
	ThinPacksApplied []string  `json:"thin_packs_applied,omitempty"` // List of applied thin packs
	RestoredAt       time.Time `json:"restored_at"`                  // Timestamp of operation
}

// PersistWithDiffsOutput extends PersistCommitOutput with diff artifacts
type PersistWithDiffsOutput struct {
	PersistCommitOutput        // Embedded commit and thin pack info
	DiffFromParentPath  string `json:"diff_from_parent_path"` // Path to diff from parent commit
	DiffFromParentSize  int64  `json:"diff_from_parent_size"` // Size of parent diff in bytes
	DiffFromBasePath    string `json:"diff_from_base_path"`   // Path to diff from base commit
	DiffFromBaseSize    int64  `json:"diff_from_base_size"`   // Size of base diff in bytes
}
