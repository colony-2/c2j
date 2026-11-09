package model

import "github.com/colony-2/c2j/pkg/jobrestart"

// Prefer ScopeID: compact histories can contain multiple scopes in one chapter.
// ChapterOrdinal remains available for unambiguous boundaries, including 0 for submission.
type GetRestartContextRequest struct {
	ProjectID      string
	JobID          string
	ChapterOrdinal int64
	ScopeID        string
}

type RestartContext = jobrestart.RestartContext
