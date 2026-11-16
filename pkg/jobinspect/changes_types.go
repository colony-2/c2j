package jobinspect

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

const ChangesVersion = 1

// ChangesAPI reads the latest persisted main workspace. It never prepares a
// review, executes a recipe, or writes artifacts. A selection is not a receipt.
type ChangesAPI interface {
	History(context.Context, ChangesRequest) (*ChangesHistory, error)
	OpenDiff(context.Context, DiffRequest) (*ChangesDiff, io.ReadCloser, error)
}

type ChangesRequest struct {
	JobID     string `json:"job_id"`
	Base      string `json:"base,omitempty"`
	Selection string `json:"selection,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}

type DiffRequest struct {
	ChangesRequest
	All     bool   `json:"all,omitempty"`
	Commit  string `json:"commit,omitempty"`
	From    string `json:"from,omitempty"`
	Through string `json:"through,omitempty"`
}

type ChangesProducer struct {
	JobID   string `json:"job_id"`
	Task    string `json:"task"`
	Attempt int    `json:"attempt"`
}

type ChangesSource struct {
	Producer ChangesProducer   `json:"producer"`
	Key      jobdb.ArtifactKey `json:"key"`
}

// ChangesIdentity reports the actual objects and retained references used.
// EmptyBase is intentional absence of a predecessor, not an unknown hash.
type ChangesIdentity struct {
	Version           int             `json:"version"`
	Tenant            string          `json:"tenant"`
	JobID             string          `json:"job_id"`
	Repository        string          `json:"repository"`
	Cell              string          `json:"cell,omitempty"`
	Scope             string          `json:"scope"`
	State             ChangesProducer `json:"state"`
	Boundary          string          `json:"boundary"`
	ActiveBase        string          `json:"active_base,omitempty"`
	ActiveBaseOrigin  string          `json:"active_base_origin"`
	Base              string          `json:"base,omitempty"`
	BaseOrigin        string          `json:"base_origin"`
	EmptyBase         bool            `json:"empty_base"`
	Unborn            bool            `json:"unborn"`
	Head              string          `json:"head,omitempty"`
	BaseTree          string          `json:"base_tree"`
	HeadTree          string          `json:"head_tree"`
	ObjectFormat      string          `json:"object_format"`
	Sources           []ChangesSource `json:"sources"`
	RepositoryObjects []string        `json:"repository_objects,omitempty"`
	SourceID          string          `json:"source_id"`
	Selection         string          `json:"selection"`
}

type ChangesPerson struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Time  string `json:"time"`
}

type ChangesCommit struct {
	Hash      string        `json:"hash"`
	Tree      string        `json:"tree"`
	Parents   []string      `json:"parents"`
	Author    ChangesPerson `json:"author"`
	Committer ChangesPerson `json:"committer"`
	Subject   string        `json:"subject"`
	Position  int           `json:"position"`
}

type ChangesHistory struct {
	ChangesIdentity
	Diff       string          `json:"diff"`    // complete or unavailable (unborn format unknown)
	History    string          `json:"history"` // complete or unsupported
	Diagnostic *ChangesError   `json:"diagnostic,omitempty"`
	Complete   bool            `json:"complete"`
	Total      int             `json:"total"`
	Commits    []ChangesCommit `json:"commits"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

type ChangesDiff struct {
	ChangesIdentity
	History    string        `json:"history"`
	Diagnostic *ChangesError `json:"diagnostic,omitempty"`
	Diff       string        `json:"diff"`
	Complete   bool          `json:"complete"`
	FromCommit string        `json:"from_commit,omitempty"`
	ToCommit   string        `json:"to_commit,omitempty"`
	FromTree   string        `json:"from_tree"`
	ToTree     string        `json:"to_tree"`
	Bytes      int64         `json:"bytes"`
}

// ChangesError is also the versioned helper/CLI diagnostic envelope. Detail
// never contains Git stderr, repository credentials, or backend response bodies.
type ChangesError struct {
	Version   int      `json:"version"`
	Code      string   `json:"code"`
	Operation string   `json:"operation,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	Missing   []string `json:"missing,omitempty"`
	Limit     string   `json:"limit,omitempty"`
	Cause     error    `json:"-"`
}

func (e *ChangesError) Error() string { return fmt.Sprintf("changes %s: %s", e.Code, e.Detail) }
func (e *ChangesError) Unwrap() error {
	if e.Cause != nil {
		return e.Cause
	}
	switch e.Code {
	case "canceled":
		return context.Canceled
	case "deadline":
		return context.DeadlineExceeded
	case "lease_lost":
		return jobdb.ErrExecutionLeaseLost
	}
	return nil
}

// ChangesDiagnostic preserves typed errors, including cancellation identity.
func ChangesDiagnostic(err error) *ChangesError {
	var e *ChangesError
	if errors.As(err, &e) {
		return e
	}
	code := "backend"
	switch {
	case errors.Is(err, context.Canceled):
		code = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		code = "deadline"
	case errors.Is(err, jobdb.ErrExecutionLeaseLost):
		code = "lease_lost"
	case errors.Is(err, jobdb.ErrCellForbidden), errors.Is(err, jobdb.ErrLeasePermissionDenied):
		code = "job_denied"
	}
	return &ChangesError{Version: ChangesVersion, Code: code, Detail: "read could not complete", Cause: err}
}

func changesErr(code, detail string) *ChangesError {
	return &ChangesError{Version: ChangesVersion, Code: code, Detail: detail}
}
func changesLimit(name string) *ChangesError {
	e := changesErr("limit", "read budget exhausted")
	e.Limit = name
	return e
}

func changesOperation(operation string, err *error) {
	if *err == nil {
		return
	}
	original := ChangesDiagnostic(*err)
	copy := *original
	if copy.Operation == "" {
		copy.Operation = operation
	}
	*err = &copy
}

// ValidateChangesDiagnostic rejects unknown wire versions and categories.
func ValidateChangesDiagnostic(e *ChangesError) error {
	if e == nil || e.Version != ChangesVersion {
		return changesErr("protocol", "unsupported changes diagnostic")
	}
	switch e.Code {
	case "invalid_request", "workspace_unavailable", "workspace_ambiguous", "stale_selection", "invalid_cursor", "invalid_range", "protocol", "unsupported_history", "missing_source", "missing_closure", "invalid_objects", "source_mismatch", "job_denied", "repository_denied", "repository_authentication", "repository_unavailable", "limit", "git_unavailable", "temporary_storage", "canceled", "deadline", "backend", "transport", "lease_lost":
		return nil
	}
	return changesErr("protocol", "unknown changes diagnostic")
}

// ValidateChangesHistory and ValidateChangesDiff guard helper/adapter protocol
// envelopes. Membership and authorization are still checked by the reader.
func ValidateChangesHistory(h *ChangesHistory) error {
	if h == nil {
		return changesErr("protocol", "missing history response")
	}
	if err := validateChangesIdentity(h.ChangesIdentity); err != nil {
		return err
	}
	if h.Commits == nil || h.Total < 0 || len(h.Commits) > h.Total {
		return changesErr("protocol", "invalid history inventory")
	}
	switch h.History {
	case "complete":
		if !h.Complete || h.Diagnostic != nil {
			return changesErr("protocol", "inconsistent complete history")
		}
	case "unsupported":
		if h.Complete || h.Total != 0 || len(h.Commits) != 0 || h.NextCursor != "" || h.Diagnostic == nil || h.Diagnostic.Code != "unsupported_history" || ValidateChangesDiagnostic(h.Diagnostic) != nil {
			return changesErr("protocol", "inconsistent unsupported history")
		}
	default:
		return changesErr("protocol", "unknown history status")
	}
	position := 0
	for _, commit := range h.Commits {
		if !fullHash(commit.Hash) || !fullHash(commit.Tree) || len(commit.Hash) != len(h.Head) || len(commit.Tree) != len(h.Head) || commit.Position <= position || (position > 0 && commit.Position != position+1) || commit.Position > h.Total || len(commit.Parents) > 1 {
			return changesErr("protocol", "invalid commit row")
		}
		position = commit.Position
		for _, parent := range commit.Parents {
			if !fullHash(parent) || len(parent) != len(h.Head) {
				return changesErr("protocol", "invalid commit parent")
			}
		}
	}
	if h.Diff != "complete" && !(h.Unborn && h.Diff == "unavailable") {
		return changesErr("protocol", "invalid diff availability")
	}
	return nil
}

func ValidateChangesDiff(m *ChangesDiff) error {
	if m == nil {
		return changesErr("protocol", "missing diff response")
	}
	if err := validateChangesIdentity(m.ChangesIdentity); err != nil {
		return err
	}
	if m.Unborn || !m.Complete || m.Diff != "complete" || m.Bytes < 0 || !fullHash(m.FromTree) || !fullHash(m.ToTree) {
		return changesErr("protocol", "invalid diff comparison")
	}
	if m.History != "complete" && m.History != "unsupported" {
		return changesErr("protocol", "unknown diff history status")
	}
	if m.History == "unsupported" && (m.Diagnostic == nil || m.Diagnostic.Code != "unsupported_history" || ValidateChangesDiagnostic(m.Diagnostic) != nil) {
		return changesErr("protocol", "missing unsupported-history diagnostic")
	}
	if m.History == "complete" && m.Diagnostic != nil {
		return changesErr("protocol", "unexpected diff diagnostic")
	}
	for _, hash := range []string{m.FromTree, m.ToTree, m.ToCommit} {
		if !fullHash(hash) || len(hash) != len(m.Head) {
			return changesErr("protocol", "invalid diff endpoint")
		}
	}
	if m.FromCommit != "" && (!fullHash(m.FromCommit) || len(m.FromCommit) != len(m.Head)) {
		return changesErr("protocol", "invalid diff predecessor")
	}
	return nil
}

func validateChangesIdentity(id ChangesIdentity) error {
	if id.Version != ChangesVersion || id.Tenant == "" || id.JobID == "" || id.Repository == "" || id.Selection == "" || id.SourceID == "" || id.State.JobID != id.JobID || id.State.Task == "" || id.State.Attempt <= 0 || id.Sources == nil {
		return changesErr("protocol", "invalid changes identity")
	}
	if id.Boundary != "before" && id.Boundary != "after" {
		return changesErr("protocol", "unknown workspace boundary")
	}
	if (id.BaseOrigin != "active" && id.BaseOrigin != "explicit") || (id.EmptyBase && id.Base != "") {
		return changesErr("protocol", "contradictory comparison base")
	}
	if id.Unborn {
		if !id.EmptyBase || id.Head != "" || id.Base != "" || id.ObjectFormat != "" || id.HeadTree != "" || id.BaseTree != "" {
			return changesErr("protocol", "invalid unborn workspace")
		}
	} else {
		size := 40
		if id.ObjectFormat == "sha256" {
			size = 64
		} else if id.ObjectFormat != "sha1" {
			return changesErr("protocol", "unknown object format")
		}
		for _, hash := range []string{id.Head, id.HeadTree, id.BaseTree} {
			if len(hash) != size || !fullHash(hash) {
				return changesErr("protocol", "invalid endpoint identity")
			}
		}
		if !id.EmptyBase && (len(id.Base) != size || !fullHash(id.Base)) {
			return changesErr("protocol", "invalid comparison base")
		}
		if id.ActiveBase != "" && (!fullHash(id.ActiveBase) || len(id.ActiveBase) != size) {
			return changesErr("protocol", "invalid active base")
		}
	}
	for _, source := range id.Sources {
		if source.Key.Validate() != nil || source.Key.SizeBytes < 0 || source.Producer.JobID != source.Key.JobId || source.Producer.Task == "" || source.Producer.Attempt <= 0 {
			return changesErr("protocol", "invalid source identity")
		}
	}
	return nil
}
