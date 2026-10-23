package model

// Prefer ScopeID: compact histories can contain multiple scopes in one chapter.
// ChapterOrdinal remains available for unambiguous historical boundaries.
type GetRestartContextRequest struct {
	ProjectID      string
	JobID          string
	ChapterOrdinal int64
	ScopeID        string
}

type RestartContext struct {
	ProjectID      string `json:"project_id"`
	JobID          string `json:"job_id"`
	ChapterOrdinal int64  `json:"chapter_ordinal"`
	ScopeID        string `json:"scope_id"`
	// Job is the before-state for ContextPatch.Job. Scoped patches address
	// Visible.sequence[id].outputs or Visible.states[id].outputs.
	Job     map[string]any `json:"job"`
	Visible map[string]any `json:"visible"`
	// RedactedPaths are JSON pointers into this response, never patch values.
	RedactedPaths []string `json:"redacted_paths"`
}
