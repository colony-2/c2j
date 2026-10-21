package model

import coretask "github.com/colony-2/c2j/pkg/task"

// RestartRecipeJobRequest requests a restart of an existing recipe job at a recorded execution scope.
//
// ScopeID is a logical scope ID from the job execution history.
// Patch is optional; when present it is applied before resuming execution.
type RestartRecipeJobRequest struct {
	ProjectID string
	JobID     string
	ScopeID   string
	// ChapterOrdinal selects a recorded restartable scope entry. Mutually
	// exclusive with ScopeID, which remains supported for existing clients.
	ChapterOrdinal *int64
	Patch          *coretask.ContextPatch
}

type RestartRecipeJobResponse struct {
	JobID string `json:"job_id"`
}
