// Package jobrestart defines the CLI and authorized-helper restart contract.
package jobrestart

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/task"
)

type Selector struct {
	Scope   string `json:"scope,omitempty"`
	Chapter *int64 `json:"chapter,omitempty"`
}

func (s Selector) Validate() error {
	if (s.Scope != "") == (s.Chapter != nil) || (s.Scope != "" && strings.TrimSpace(s.Scope) == "") {
		return fmt.Errorf("exactly one nonblank --scope or explicit --chapter is required")
	}
	if s.Chapter != nil && *s.Chapter < 0 {
		return fmt.Errorf("chapter must not be negative")
	}
	return nil
}

type Points struct {
	ProjectID     string         `json:"project_id"`
	JobID         string         `json:"job_id"`
	RestartScopes []RestartScope `json:"restart_scopes"`
}
type Request struct {
	Operation string             `json:"operation"`
	JobID     string             `json:"job_id"`
	Selector  Selector           `json:"selector"`
	Patch     *task.ContextPatch `json:"patch,omitempty"`
	Goal      string             `json:"goal,omitempty"`
	RequestID string             `json:"request_id,omitempty"`
}
type Response struct {
	Points  *Points         `json:"points,omitempty"`
	Context *RestartContext `json:"context,omitempty"`
	Ref     *jobref.Ref     `json:"ref,omitempty"`
}
type API interface {
	Restart(context.Context, Request) (*Response, error)
}

type RestartContext struct {
	ProjectID      string `json:"project_id"`
	JobID          string `json:"job_id"`
	ChapterOrdinal int64  `json:"chapter_ordinal"`
	ScopeID        string `json:"scope_id"`
	// Job is the before-state for ContextPatch.Job. Scoped patches address
	// Visible.sequence[id].outputs or Visible.states[id].outputs.
	// At submission these are read-only: Job is the submitted context and
	// Visible contains only the submitted inputs, before defaults or setup.
	Job     map[string]any `json:"job"`
	Visible map[string]any `json:"visible"`
	// RedactedPaths are JSON pointers into this response, never patch values.
	RedactedPaths []string `json:"redacted_paths"`
}

type RestartScope struct {
	UnavailableReason string    `json:"unavailable_reason,omitempty"`
	ChapterOrdinal    int64     `json:"chapter_ordinal"`
	Title             string    `json:"title,omitempty"`
	Path              []string  `json:"path,omitempty"`
	ScopeID           string    `json:"scope_id"`
	StartedAt         time.Time `json:"started_at"`
}
type apiKey struct{}

func WithAPI(ctx context.Context, api API) context.Context {
	return context.WithValue(ctx, apiKey{}, api)
}
func FromContext(ctx context.Context) API { api, _ := ctx.Value(apiKey{}).(API); return api }
