package jobinspect

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// LeaseEventOptions selects one page of retained history. Tenant and job scope
// are fixed by Service and the selected job, never by these options.
type LeaseEventOptions struct {
	EventTypes     []jobdb.LeaseEventType `json:"event_types,omitzero"`
	OccurredAfter  *time.Time             `json:"occurred_after,omitempty"`
	OccurredBefore *time.Time             `json:"occurred_before,omitempty"`
	IncludeHidden  bool                   `json:"include_hidden,omitempty"`
	PageSize       int                    `json:"page_size,omitempty"`
	PageToken      string                 `json:"page_token,omitempty"`
}

// UnmarshalJSON retains JobDB's distinction between an omitted event filter
// and an explicitly null/empty array, including through the task helper.
func (o *LeaseEventOptions) UnmarshalJSON(raw []byte) error {
	type plain LeaseEventOptions
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if _, supplied := fields["event_types"]; supplied && len(decoded.EventTypes) == 0 {
		return jobdb.ErrInvalidLeaseEventRequest
	}
	*o = LeaseEventOptions(decoded)
	return nil
}

// ListJobLeaseEvents returns exactly one backend page without enrichment or
// traversal. Unknown jobs and jobs without visible retained history return an
// empty page under JobDB's listing policy; no existence probe is performed.
// Resolve the CLI's "self" alias before calling this method.
func (s *Service) ListJobLeaseEvents(ctx context.Context, jobID string, options LeaseEventOptions) (jobdb.ListLeaseEventsResponse, error) {
	if s == nil || s.Runtime == nil || strings.TrimSpace(s.Tenant) == "" || strings.TrimSpace(jobID) == "" || jobID == "self" {
		return jobdb.ListLeaseEventsResponse{}, jobdb.ErrInvalidLeaseEventRequest
	}
	if ctx == nil {
		return jobdb.ListLeaseEventsResponse{}, jobdb.ErrInvalidLeaseEventRequest
	}
	if err := ctx.Err(); err != nil {
		return jobdb.ListLeaseEventsResponse{}, err
	}
	if s.Authorize != nil {
		if err := s.Authorize(ctx, jobID); err != nil {
			return jobdb.ListLeaseEventsResponse{}, err
		}
	}
	return s.Runtime.ListLeaseEvents(ctx, jobdb.ListLeaseEventsRequest{
		TenantID: s.Tenant, JobIDs: []string{jobID}, EventTypes: options.EventTypes,
		OccurredAfter: options.OccurredAfter, OccurredBefore: options.OccurredBefore,
		IncludeHidden: options.IncludeHidden, PageSize: options.PageSize, PageToken: options.PageToken,
	})
}
