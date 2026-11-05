package model

import coretask "github.com/colony-2/c2j/pkg/task"

// RestartRecipeJobRequest restarts a recipe job at submission or a recorded execution scope.
//
// ScopeID is a boundary ID returned in JobRunStory.RestartScopes.
// Patch is optional and supported at recipe scopes, but not at submission.
type RestartRecipeJobRequest struct {
	RequestID string
	Goal      struct {
		Mode string `json:"mode"`
	}
	ProjectID string
	JobID     string
	ScopeID   string
	// ChapterOrdinal selects a recorded restartable scope, or 0 for submission. Mutually
	// exclusive with ScopeID, which remains supported for existing clients.
	ChapterOrdinal *int64
	Patch          *coretask.ContextPatch
}

type RestartRecipeJobResponse struct {
	GoalID   string `json:"goal_id"`
	TenantID string `json:"tenant_id"`
	JobID    string `json:"job_id"`
}
