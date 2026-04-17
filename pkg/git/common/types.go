package common

import "time"

// GitConfig contains common Git configuration
type GitConfig struct {
	Author  string        `json:"author,omitempty"`
	Email   string        `json:"email,omitempty"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

// ThinPackMetadata describes a thin pack file
type ThinPackMetadata struct {
	CommitHash string    `json:"commit_hash"`
	ParentHash string    `json:"parent_hash"`
	RootHash   string    `json:"root_hash"`
	FilePath   string    `json:"file_path"`
	Size       int64     `json:"size"`
	CreatedAt  time.Time `json:"created_at"`
}
