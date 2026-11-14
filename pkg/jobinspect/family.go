package jobinspect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// FamilyOptions also prevents older, strict task helpers from falling through
// to history loading for an operation they do not understand.
type FamilyOptions struct {
	Version int `json:"version"`
}

func (o *FamilyOptions) UnmarshalJSON(raw []byte) error {
	type plain FamilyOptions
	var decoded plain
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&decoded); err != nil {
		return err
	}
	*o = FamilyOptions(decoded)
	return nil
}

type FamilyNode struct {
	ID               string         `json:"id"`
	Tenant           string         `json:"tenant"`
	Parent           string         `json:"parent,omitempty"`
	Cell             string         `json:"cell,omitempty"`
	JobType          string         `json:"job_type"`
	Title            string         `json:"title,omitempty"`
	Goal             string         `json:"goal,omitempty"`
	Status           string         `json:"status"`
	CompletionStatus string         `json:"completion_status,omitempty"`
	CancelRequested  bool           `json:"cancel_requested"`
	CreatedAt        time.Time      `json:"created_at"`
	Store            jobdb.JobStore `json:"store"`
	ArchivedAt       *time.Time     `json:"archived_at,omitempty"`
}
type FamilyPage struct {
	Version    int          `json:"version"`
	Nodes      []FamilyNode `json:"nodes"`
	NextCursor string       `json:"next_cursor"`
}

var ErrFamilyProtocol = errors.New("unsupported family inspection protocol")

func familyProtocolError() error {
	return fmt.Errorf("%w; upgrade the worker and CLI together", ErrFamilyProtocol)
}

func ValidateFamilyRequest(q Request) error {
	if q.Operation != "family-node" && q.Operation != "family-children" {
		return fmt.Errorf("invalid family operation")
	}
	if q.Family == nil || q.Family.Version != 1 {
		return fmt.Errorf("family protocol version 1 required")
	}
	if strings.TrimSpace(q.JobID) == "" || q.JobID == "self" {
		return fmt.Errorf("explicit job ID required")
	}
	if q.Limit < 1 || q.Limit > 100 {
		return fmt.Errorf("family page size must be between 1 and 100")
	}
	if q.Operation == "family-node" && q.Cursor != "" {
		return fmt.Errorf("exact family lookup cannot have a cursor")
	}
	rest := q
	rest.Operation, rest.JobID, rest.Cursor, rest.Limit, rest.Family = "", "", "", 0, nil
	if !reflect.DeepEqual(rest, Request{}) {
		return fmt.Errorf("unrelated selectors are not allowed for family reads")
	}
	return nil
}

func (s *Service) family(ctx context.Context, q Request) (*Response, error) {
	if err := ValidateFamilyRequest(q); err != nil {
		return nil, err
	}
	if ctx == nil || s == nil || s.Runtime == nil || strings.TrimSpace(s.Tenant) == "" {
		return nil, fmt.Errorf("family inspection requires runtime, tenant and context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Authorize != nil {
		if err := s.Authorize(ctx, q.JobID); err != nil {
			return nil, err
		}
	}
	req := jobdb.ListJobsRequest{TenantIds: []string{s.Tenant}, IncludeHidden: true, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}, PageSize: q.Limit, PageToken: q.Cursor}
	if q.Operation == "family-node" {
		req.JobKeys = []jobdb.JobKey{{TenantId: s.Tenant, JobId: q.JobID}}
	} else {
		req.ParentJobIDs = []string{q.JobID}
	}
	page, err := s.Runtime.ListJobs(ctx, req)
	if err != nil {
		return nil, err
	}
	out := &FamilyPage{Version: 1, Nodes: []FamilyNode{}, NextCursor: page.NextPageToken}
	for _, j := range page.Jobs {
		var meta struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(j.Metadata, &meta) != nil {
			meta.Title = ""
		}
		store := jobdb.JobStoreActive
		if j.ArchivedAt != nil {
			store = jobdb.JobStoreArchived
		}
		out.Nodes = append(out.Nodes, FamilyNode{ID: j.JobKey.JobId, Tenant: j.JobKey.TenantId, Parent: j.ParentJobID, Cell: j.Cell, JobType: j.JobType, Title: meta.Title, Goal: j.GoalID, Status: string(j.Status), CompletionStatus: j.CompletionStatus, CancelRequested: j.CancelRequested, CreatedAt: j.CreatedAt, Store: store, ArchivedAt: j.ArchivedAt})
	}
	if q.Operation == "family-node" {
		if len(out.Nodes) == 0 {
			return nil, fmt.Errorf("job %q unavailable under current visibility", q.JobID)
		}
		if len(out.Nodes) != 1 || out.NextCursor != "" || out.Nodes[0].Tenant != s.Tenant || out.Nodes[0].ID != q.JobID {
			return nil, &jobdb.CellError{Code: "invalid_identity", Status: 502}
		}
	}
	return &Response{Version: Version, Family: out}, nil
}

// ValidateFamilyResponse is used by both direct and task-helper callers. No
// legacy show/list projection is a substitute for these compact summaries.
func ValidateFamilyResponse(r *Response) error {
	if r == nil || r.Version != Version || r.Family == nil || r.Family.Version != 1 || r.Family.Nodes == nil {
		return familyProtocolError()
	}
	return nil
}

// FamilyErrorReason reports only typed safe codes, never inferred denial from
// arbitrary backend text. It is also the helper's safe error vocabulary.
func FamilyErrorReason(err error) string {
	switch {
	case errors.Is(err, ErrFamilyProtocol):
		return "unsupported_protocol"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, jobdb.ErrExecutionLeaseLost):
		return "lease_lost"
	case errors.Is(err, jobdb.ErrLeasePermissionDenied):
		return "permission_denied"
	}
	if _, code, ok := jobdb.CellErrorStatus(err); ok {
		return code
	}
	return "unavailable"
}
