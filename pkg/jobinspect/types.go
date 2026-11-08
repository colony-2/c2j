// Package jobinspect exposes read-only, persisted job inspection without recipe
// loading or workflow replay. CLI and task helpers share this contract.
package jobinspect

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const Version = 1

type Request struct {
	Events    LeaseEventOptions  `json:"events,omitzero"`
	Operation string             `json:"operation"`
	JobID     string             `json:"job_id,omitempty"`
	Task      string             `json:"task,omitempty"`
	Attempt   int                `json:"attempt,omitempty"`
	Limit     int                `json:"limit,omitempty"`
	Cursor    string             `json:"cursor,omitempty"`
	Field     *string            `json:"field,omitempty"`
	Name      string             `json:"name,omitempty"`
	Reference *jobdb.ArtifactKey `json:"reference,omitempty"`
	Workspace string             `json:"workspace,omitempty"`
	Boundary  string             `json:"boundary,omitempty"`
	Parent    string             `json:"parent,omitempty"`
	Status    []jobdb.JobStatus  `json:"status,omitempty"`
}

type API interface {
	Inspect(context.Context, Request) (*Response, error)
	Download(context.Context, Request) (io.ReadCloser, error)
}

type Response struct {
	Version     int                            `json:"version"`
	LeaseEvents *jobdb.ListLeaseEventsResponse `json:"lease_events,omitempty"`
	Job         *Job                           `json:"job,omitempty"`
	Jobs        []Job                          `json:"jobs,omitempty"`
	Tasks       []Task                         `json:"tasks,omitempty"`
	Task        *Task                          `json:"task,omitempty"`
	Scopes      []Scope                        `json:"scopes,omitempty"`
	Result      *Result                        `json:"result,omitempty"`
	Artifacts   []Artifact                     `json:"artifacts,omitempty"`
	Fields      []Field                        `json:"fields,omitempty"`
	Snapshot    *Snapshot                      `json:"snapshot,omitempty"`
	Value       json.RawMessage                `json:"value,omitempty"`
	NextCursor  string                         `json:"next_cursor,omitempty"`
	Warnings    []string                       `json:"warnings,omitempty"`
}

type Job struct {
	ID               string    `json:"id"`
	Tenant           string    `json:"tenant"`
	Cell             string    `json:"cell,omitempty"`
	Goal             string    `json:"goal,omitempty"`
	Parent           string    `json:"parent,omitempty"`
	Status           string    `json:"status"`
	CompletionStatus string    `json:"completion_status,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type Field struct {
	Pointer   string `json:"pointer"`
	Type      string `json:"type"`
	Bytes     int    `json:"bytes"`
	Preview   string `json:"preview"`
	Truncated bool   `json:"truncated,omitempty"`
}

type Task struct {
	ID                 string          `json:"id"`
	Type               string          `json:"type"`
	Path               string          `json:"path,omitempty"`
	Cell               string          `json:"cell,omitempty"`
	Workspace          string          `json:"workspace,omitempty"`
	Attempt            int             `json:"attempt"`
	Attempts           []int           `json:"attempts,omitempty"`
	Status             string          `json:"status"`
	StartedAt          *time.Time      `json:"started_at,omitempty"`
	FinishedAt         *time.Time      `json:"finished_at,omitempty"`
	Error              json.RawMessage `json:"error,omitempty"`
	OutputAvailability string          `json:"output_availability"`
	Fields             []Field         `json:"fields,omitempty"`
	Artifacts          []Artifact      `json:"artifacts,omitempty"`
	InventoryTruncated bool            `json:"inventory_truncated,omitempty"`
}

type Scope struct {
	ID     string `json:"id"`
	Parent string `json:"parent,omitempty"`
	Site   string `json:"site,omitempty"`
	Status string `json:"status"`
}

type Result struct {
	Availability       string     `json:"availability"`
	Fields             []Field    `json:"fields,omitempty"`
	Artifacts          []Artifact `json:"artifacts,omitempty"`
	InventoryTruncated bool       `json:"inventory_truncated,omitempty"`
}

type Artifact struct {
	Name         string        `json:"name"`
	Task         string        `json:"task,omitempty"`
	Attempt      int           `json:"attempt,omitempty"`
	Reference    artifacts.Ref `json:"reference"`
	Size         int64         `json:"size"`
	Availability string        `json:"availability"`
	Path         string        `json:"path,omitempty"`
}

type Workspace struct {
	coretask.WorkspaceState
	Task          string    `json:"task,omitempty"`
	Attempt       int       `json:"attempt,omitempty"`
	Boundary      string    `json:"boundary"`
	State         string    `json:"state"`
	Availability  string    `json:"availability"`
	Reason        string    `json:"reason,omitempty"`
	Format        string    `json:"format,omitempty"`
	Bundle        *Artifact `json:"bundle,omitempty"`
	Prerequisites []string  `json:"prerequisites,omitempty"`
	Directory     string    `json:"directory,omitempty"`
}

type Snapshot struct {
	Version    int         `json:"version"`
	JobID      string      `json:"job_id"`
	Task       string      `json:"task,omitempty"`
	Attempt    int         `json:"attempt,omitempty"`
	Workspaces []Workspace `json:"workspaces"`
	Artifacts  []Artifact  `json:"artifacts"`
}

type contextKey struct{}

func WithAPI(ctx context.Context, api API) context.Context {
	return context.WithValue(ctx, contextKey{}, api)
}
func FromContext(ctx context.Context) API { api, _ := ctx.Value(contextKey{}).(API); return api }
