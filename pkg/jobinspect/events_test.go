package jobinspect_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

// Embedding a nil runtime makes every unexpected read or mutation panic.
type eventRecorder struct {
	jobdb.WorkflowRuntime
	requests []jobdb.ListLeaseEventsRequest
	page     jobdb.ListLeaseEventsResponse
	err      error
}

func (r *eventRecorder) ListLeaseEvents(_ context.Context, req jobdb.ListLeaseEventsRequest) (jobdb.ListLeaseEventsResponse, error) {
	r.requests = append(r.requests, req)
	return r.page, r.err
}

func TestJobLeaseEventsScopeAndPage(t *testing.T) {
	at := time.Date(2026, 11, 8, 1, 2, 3, 123456789, time.UTC)
	before := at.Add(time.Hour)
	page := jobdb.ListLeaseEventsResponse{Events: []jobdb.LeaseEvent{
		{EventID: "9007199254740995", EventType: jobdb.LeaseEventRenewed, OccurredAt: at, PreviousLeaseExpiresAt: &before, WorkerID: "historical", Cell: "old"},
		{EventID: "9007199254740994", EventType: jobdb.LeaseEventAcquired, OccurredAt: at},
	}, NextPageToken: "opaque+/= token"}
	rt := &eventRecorder{page: page}
	authorized := 0
	service := &jobinspect.Service{Runtime: rt, Tenant: "fixed", Authorize: func(_ context.Context, id string) error { require.Equal(t, "selected", id); authorized++; return nil }}
	options := jobinspect.LeaseEventOptions{PageSize: 2, PageToken: "input-token", EventTypes: []jobdb.LeaseEventType{jobdb.LeaseEventRenewed}, OccurredAfter: &at, OccurredBefore: &before, IncludeHidden: true}
	response, err := service.Inspect(context.Background(), jobinspect.Request{Operation: "events", JobID: "selected", Events: options})
	require.NoError(t, err)
	require.Equal(t, page, *response.LeaseEvents)
	require.Equal(t, 1, authorized)
	require.Len(t, rt.requests, 1)
	require.Equal(t, jobdb.ListLeaseEventsRequest{TenantID: "fixed", JobIDs: []string{"selected"}, EventTypes: options.EventTypes, OccurredAfter: &at, OccurredBefore: &before, IncludeHidden: true, PageSize: 2, PageToken: "input-token"}, rt.requests[0])
}

func TestJobLeaseEventsEmptyErrorsAndDenied(t *testing.T) {
	rt := &eventRecorder{page: jobdb.ListLeaseEventsResponse{Events: []jobdb.LeaseEvent{}}}
	service := &jobinspect.Service{Runtime: rt, Tenant: "tenant"}
	page, err := service.ListJobLeaseEvents(context.Background(), "unknown", jobinspect.LeaseEventOptions{})
	require.NoError(t, err)
	require.NotNil(t, page.Events)
	require.Empty(t, page.Events)
	for _, failure := range []error{jobdb.ErrInvalidLeaseEventRequest, jobdb.ErrInvalidLeaseEventPageToken, jobdb.ErrCellForbidden, errors.New("authentication failed"), errors.New("backend unavailable")} {
		rt.err = failure
		_, err = service.ListJobLeaseEvents(context.Background(), "job", jobinspect.LeaseEventOptions{})
		require.ErrorIs(t, err, failure)
	}
	calls := len(rt.requests)
	service.Authorize = func(context.Context, string) error { return jobdb.ErrCellForbidden }
	_, err = service.ListJobLeaseEvents(context.Background(), "denied", jobinspect.LeaseEventOptions{})
	require.ErrorIs(t, err, jobdb.ErrCellForbidden)
	require.Len(t, rt.requests, calls)
	for _, id := range []string{"", " ", "self"} {
		_, err = service.ListJobLeaseEvents(context.Background(), id, jobinspect.LeaseEventOptions{})
		require.ErrorIs(t, err, jobdb.ErrInvalidLeaseEventRequest)
	}
	require.Len(t, rt.requests, calls)
}
