package jobinspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const WorkspaceDiffVersion = 1

// WorkspaceDiffAPI reads original saved cumulative comparisons. Reading through
// EOF and closing successfully is required to validate a complete download.
// Unavailable comparisons return metadata, a nil body and a nil error.
type WorkspaceDiffAPI interface {
	OpenWorkspaceDiff(context.Context, WorkspaceDiffRequest) (*WorkspaceDiff, io.ReadCloser, error)
}

// WorkspaceDiffOptions preserves omission independently of explicit defaults.
// IDs are native occurrence IDs, as returned by log and story, not node names.
type WorkspaceDiffOptions struct {
	Task      *string `json:"task,omitempty"`
	Scope     *string `json:"scope,omitempty"`
	Attempt   *int    `json:"attempt,omitempty"`
	Workspace *string `json:"workspace,omitempty"`
	Boundary  *string `json:"boundary,omitempty"`
}

type WorkspaceDiffRequest struct {
	JobID string `json:"job_id"`
	WorkspaceDiffOptions
}

type WorkspaceDiffPoint struct {
	Task      string `json:"task,omitempty"`
	Scope     string `json:"scope,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
	Status    string `json:"status"`
	Boundary  string `json:"boundary"`
	Workspace string `json:"workspace,omitempty"`
}

type WorkspaceDiffProducer struct {
	JobID   string `json:"job_id"`
	Task    string `json:"task"`
	Attempt int    `json:"attempt"`
}

type WorkspaceDiffSource struct {
	Producer WorkspaceDiffProducer `json:"producer"`
	Key      jobdb.ArtifactKey     `json:"key"`
}

type WorkspaceDiff struct {
	Version       int                      `json:"version"`
	Status        string                   `json:"status"` // available, empty, unavailable
	Requested     WorkspaceDiffRequest     `json:"requested"`
	Resolved      WorkspaceDiffPoint       `json:"resolved"`
	Observation   string                   `json:"observation"`
	Cutoff        string                   `json:"cutoff"`
	Workspace     *coretask.WorkspaceState `json:"workspace,omitempty"`
	StateProducer *WorkspaceDiffProducer   `json:"state_producer,omitempty"`
	Source        *WorkspaceDiffSource     `json:"source,omitempty"`
	Bytes         *int64                   `json:"bytes,omitempty"` // absent means unknown
	Reasons       []string                 `json:"reasons,omitempty"`
}

// WorkspaceDiffError keeps operational failures distinct from unavailability.
// Unwrap preserves cancellation, deadline and lease error identities locally.
type WorkspaceDiffError struct {
	Version int    `json:"version"`
	Code    string `json:"code"`
	Detail  string `json:"detail"`
	Cause   error  `json:"-"`
}

func (e *WorkspaceDiffError) Error() string { return e.Code + ": " + e.Detail }
func (e *WorkspaceDiffError) Unwrap() error { return e.Cause }

func WorkspaceDiffDiagnostic(code, detail string, cause error) *WorkspaceDiffError {
	var typed *WorkspaceDiffError
	if errors.As(cause, &typed) {
		return typed
	}
	switch {
	case errors.Is(cause, context.Canceled):
		code = "canceled"
	case errors.Is(cause, context.DeadlineExceeded):
		code = "timeout"
	case errors.Is(cause, jobdb.ErrExecutionLeaseLost):
		code = "lease_lost"
	case errors.Is(cause, jobdb.ErrLeasePermissionDenied):
		code = "denied"
	}
	return &WorkspaceDiffError{Version: WorkspaceDiffVersion, Code: code, Detail: detail, Cause: cause}
}

func (q WorkspaceDiffRequest) Validate() error {
	bad := func(detail string) error {
		return WorkspaceDiffDiagnostic("invalid_request", detail+"; use job log and job story for exact occurrence IDs", nil)
	}
	if strings.TrimSpace(q.JobID) == "" {
		return bad("job_id is required")
	}
	if q.Task != nil && q.Scope != nil {
		return bad("task and scope are mutually exclusive")
	}
	for _, v := range []*string{q.Task, q.Scope, q.Workspace, q.Boundary} {
		if v != nil && strings.TrimSpace(*v) == "" {
			return bad("explicit selectors cannot be blank")
		}
	}
	if q.Task == nil && q.Scope == nil && (q.Attempt != nil || q.Boundary != nil) {
		return bad("attempt and boundary require a task or scope")
	}
	if q.Attempt != nil && *q.Attempt <= 0 {
		return bad("attempt must be positive")
	}
	if q.Boundary != nil && *q.Boundary != "before" && *q.Boundary != "after" {
		return bad("boundary must be before or after")
	}
	return nil
}

// strictWorkspaceObject rejects duplicates and nulls before pointer decoding can
// erase presence. It is deliberately used only by the saved-diff operation.
func strictWorkspaceObject(raw []byte, allowed map[string]bool) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("expected object")
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		t, err = d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := t.(string)
		if !ok {
			return nil, fmt.Errorf("expected key")
		}
		if _, exists := out[key]; exists || !allowed[key] {
			return nil, fmt.Errorf("duplicate or unsupported field %q", key)
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(value)) == "null" {
			return nil, fmt.Errorf("field %q cannot be null", key)
		}
		out[key] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	return out, nil
}

func (o *WorkspaceDiffOptions) UnmarshalJSON(raw []byte) error {
	if _, err := strictWorkspaceObject(raw, map[string]bool{"task": true, "scope": true, "attempt": true, "workspace": true, "boundary": true}); err != nil {
		return WorkspaceDiffDiagnostic("invalid_request", err.Error(), nil)
	}
	type plain WorkspaceDiffOptions
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*o = WorkspaceDiffOptions(decoded)
	return nil
}

func (q *WorkspaceDiffRequest) UnmarshalJSON(raw []byte) error {
	fields, err := strictWorkspaceObject(raw, map[string]bool{"job_id": true, "task": true, "scope": true, "attempt": true, "workspace": true, "boundary": true})
	if err != nil {
		return WorkspaceDiffDiagnostic("invalid_request", err.Error(), nil)
	}
	var decoded WorkspaceDiffRequest
	if job, ok := fields["job_id"]; ok {
		if err := json.Unmarshal(job, &decoded.JobID); err != nil {
			return err
		}
	}
	delete(fields, "job_id")
	opts, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(opts, &decoded.WorkspaceDiffOptions); err != nil {
		return err
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*q = decoded
	return nil
}

func validateWorkspaceInspection(q Request) error {
	if q.Task != "" || q.Attempt != 0 || q.Limit != 0 || q.Cursor != "" || q.Field != nil || q.Name != "" || q.Reference != nil || q.Workspace != "" || q.Boundary != "" || q.Parent != "" || len(q.Status) != 0 || q.Metrics != nil || q.Family != nil || !reflect.DeepEqual(q.Events, LeaseEventOptions{}) {
		return WorkspaceDiffDiagnostic("invalid_request", "workspace_diff requires dedicated options; generic inspection selectors are unsupported", nil)
	}
	o := WorkspaceDiffOptions{}
	if q.WorkspaceDiff != nil {
		o = *q.WorkspaceDiff
	}
	return (WorkspaceDiffRequest{JobID: q.JobID, WorkspaceDiffOptions: o}).Validate()
}

// ValidateWorkspaceDiff validates metadata before an authenticated consumer
// follows its exact artifact reference. It never accepts a different request.
func ValidateWorkspaceDiff(q WorkspaceDiffRequest, m *WorkspaceDiff) error {
	bad := func() error {
		return WorkspaceDiffDiagnostic("protocol", "invalid saved-diff response; upgrade worker and CLI together", nil)
	}
	if m == nil || m.Version != WorkspaceDiffVersion || !reflect.DeepEqual(m.Requested, q) || m.Observation == "" || m.Cutoff == "" {
		return bad()
	}
	boundary := "after"
	if q.Boundary != nil {
		boundary = *q.Boundary
	}
	if m.Resolved.Boundary != boundary {
		return bad()
	}
	if q.Task != nil && m.Resolved.Task != *q.Task || q.Scope != nil && m.Resolved.Scope != *q.Scope || q.Attempt != nil && m.Resolved.Attempt != *q.Attempt || q.Boundary != nil && m.Resolved.Boundary != *q.Boundary {
		return bad()
	}
	if m.Resolved.Status == "" {
		return bad()
	}
	if m.Bytes != nil && *m.Bytes < 0 {
		return bad()
	}
	if m.Status == "unavailable" {
		if len(m.Reasons) == 0 || m.Source != nil || m.Bytes != nil {
			return bad()
		}
		return nil
	}
	if (m.Status != "available" && m.Status != "empty") || m.Workspace == nil || !diffStateValid(*m.Workspace) || m.StateProducer == nil {
		return bad()
	}
	if m.Resolved.Workspace != m.Workspace.ScopeID || (q.Workspace != nil && *q.Workspace != "main" && *q.Workspace != m.Workspace.ScopeID) {
		return bad()
	}
	if m.StateProducer.JobID != q.JobID || m.StateProducer.Task == "" || m.StateProducer.Attempt <= 0 {
		return bad()
	}
	if m.Source == nil {
		if m.Status != "empty" || m.Bytes == nil || *m.Bytes != 0 || !(m.Workspace.BaseCommit != "" && m.Workspace.BaseCommit == m.Workspace.HeadCommit || m.Workspace.EmptyBase && m.Workspace.HeadCommit == "") {
			return bad()
		}
		return nil
	}
	s := m.Source
	if s.Key.Validate() != nil || s.Producer.JobID != s.Key.JobId || s.Producer.Task == "" || s.Producer.Attempt <= 0 || (s.Key.Name != "diff_from_base.diff" && s.Key.Name != "diff_from_parent.diff") {
		return bad()
	}
	if s.Key.SizeBytes >= 0 && (m.Bytes == nil || *m.Bytes != s.Key.SizeBytes) {
		return bad()
	}
	if m.Status == "empty" && (m.Bytes == nil || *m.Bytes != 0) || m.Status == "available" && m.Bytes != nil && *m.Bytes == 0 {
		return bad()
	}
	return nil
}
