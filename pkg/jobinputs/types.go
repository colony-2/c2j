// Package jobinputs defines the input-independent task history transport.
package jobinputs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

const Version = 1

type Request struct {
	Version    int    `json:"version"`
	Operation  string `json:"operation"`
	JobID      string `json:"job_id"`
	Limit      int    `json:"limit,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
	Snapshot   string `json:"snapshot,omitempty"`
	EntryID    string `json:"entry_id,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
}

func (q Request) Validate() error {
	if q.Version != Version {
		return Diagnostic("unsupported", "history protocol requires version 1; upgrade worker and CLI together", nil)
	}
	if strings.TrimSpace(q.JobID) == "" {
		return Diagnostic("invalid_request", "job is required", nil)
	}
	switch q.Operation {
	case "list":
		if q.Limit < 0 || q.Limit > 500 || q.EntryID != "" || q.ArtifactID != "" || q.Snapshot != "" {
			return Diagnostic("invalid_request", "list requires limit 1..500 and optional cursor only", nil)
		}
	case "show", "artifact":
		if q.Snapshot == "" || q.EntryID == "" || q.Limit != 0 || q.Cursor != "" || (q.Operation == "artifact") != (q.ArtifactID != "") {
			return Diagnostic("invalid_request", "snapshot, entry and applicable member selectors are required", nil)
		}
	default:
		return Diagnostic("invalid_request", "unknown history operation", nil)
	}
	return nil
}

type Response struct {
	Version int             `json:"version"`
	Page    json.RawMessage `json:"page,omitempty"`
	Entry   json.RawMessage `json:"entry,omitempty"`
}

func (r *Response) Validate(op string) error {
	if r == nil || r.Version != Version {
		return Diagnostic("unsupported", "history version unsupported; upgrade worker and CLI together", nil)
	}
	raw := r.Page
	if op == "show" {
		raw = r.Entry
	}
	if op != "list" && op != "show" || len(raw) == 0 || (len(r.Page) > 0 && len(r.Entry) > 0) {
		return Diagnostic("protocol", "expected exactly one native history payload", nil)
	}
	var identity struct {
		JobID        string `json:"job_id"`
		EntryID      string `json:"entry_id"`
		Snapshot     string `json:"snapshot_token"`
		Completeness string `json:"completeness"`
	}
	if json.Unmarshal(raw, &identity) != nil || identity.Snapshot == "" || (identity.Completeness != "complete" && identity.Completeness != "partial") || (op == "list" && identity.JobID == "") || (op == "show" && identity.EntryID == "") {
		return Diagnostic("protocol", "invalid native history identifiers", nil)
	}
	return nil
}
func DecodeResponse(r io.Reader, op string) (*Response, error) {
	var v Response
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return nil, Diagnostic("protocol", "invalid history JSON", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, Diagnostic("protocol", "expected one complete history response", nil)
	}
	if err := v.Validate(op); err != nil {
		return nil, err
	}
	return &v, nil
}

type Error struct {
	Version       int    `json:"version"`
	Code          string `json:"code"`
	Detail        string `json:"detail"`
	JobID         string `json:"job_id,omitempty"`
	EntryID       string `json:"entry_id,omitempty"`
	ArtifactID    string `json:"artifact_id,omitempty"`
	BackendCode   string `json:"backend_code,omitempty"`
	BackendStatus int    `json:"backend_status,omitempty"`
	Cause         error  `json:"-"`
}

func (e *Error) Error() string { return fmt.Sprintf("input history %s: %s", e.Code, e.Detail) }
func (e *Error) Unwrap() error { return e.Cause }
func Diagnostic(code, detail string, cause error) *Error {
	// Authority causes take precedence over storage/membership wrappers.
	switch {
	case errors.Is(cause, jobdb.ErrLeasePermissionDenied):
		code = "denied"
	case errors.Is(cause, jobdb.ErrExecutionLeaseLost):
		code = "lease_lost"
	case errors.Is(cause, context.DeadlineExceeded):
		code = "timeout"
	case errors.Is(cause, context.Canceled):
		code = "canceled"
	default:
		var typed *Error
		if errors.As(cause, &typed) {
			c := *typed
			c.Cause = cause
			return &c
		}
	}
	e := &Error{Version: Version, Code: code, Detail: detail, Cause: cause}
	if status, c, ok := jobdb.CellErrorStatus(cause); ok {
		e.BackendStatus = status
		e.BackendCode = c
		if status == 401 || status == 403 {
			e.Code = "denied"
		}
	}
	return e
}
func DecodeError(raw []byte) (*Error, error) {
	var e Error
	if json.Unmarshal(raw, &e) != nil || e.Version != Version || e.Code == "" || e.Detail == "" {
		return nil, Diagnostic("protocol", "invalid history diagnostic", nil)
	}
	switch e.Code {
	case "denied":
		e.Cause = jobdb.ErrLeasePermissionDenied
	case "lease_lost":
		e.Cause = jobdb.ErrExecutionLeaseLost
	case "canceled":
		e.Cause = context.Canceled
	case "timeout":
		e.Cause = context.DeadlineExceeded
	}
	return &e, nil
}

type API interface {
	Inspect(context.Context, Request) (*Response, error)
	Open(context.Context, Request) (*Artifact, error)
	Check(context.Context, string) error
}

// Artifact carries the exact retained member size across the input-independent
// helper boundary. SizeBytes is not inferred from downloaded bytes.
type Artifact struct {
	io.ReadCloser
	SizeBytes int64
}

// Authority is constructed only by worker composition, never from a request.
type Authority struct {
	Tenant string
	Guard  func(context.Context, string) error
	Bind   func(context.Context) (context.Context, context.CancelFunc)
}
type Factory func(Authority) (API, error)
type authorityFactory func() (API, error)
type factoryKey struct{}

func WithFactory(ctx context.Context, f func() (API, error)) context.Context {
	return context.WithValue(ctx, factoryKey{}, authorityFactory(f))
}
func NewFromContext(ctx context.Context) (API, error) {
	f, _ := ctx.Value(factoryKey{}).(authorityFactory)
	if f == nil {
		return nil, nil
	}
	return f()
}
