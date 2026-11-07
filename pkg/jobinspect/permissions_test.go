package jobinspect_test

import (
	"context"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type listingRecorder struct {
	jobdb.WorkflowRuntime
	requests []jobdb.ListJobsRequest
}

func (r *listingRecorder) ListJobs(_ context.Context, q jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	r.requests = append(r.requests, q)
	return jobdb.ListJobsResponse{}, nil
}

// INS-008–009: defaults are filters and authorization is unconditional.
func TestInspectionDiscoveryAuthorizationAndFilters(t *testing.T) {
	for _, tc := range []struct{ defaultParent, requested, want string }{{"", "", ""}, {"default", "", "default"}, {"default", "other", "other"}, {"", "other", "other"}} {
		t.Run(tc.defaultParent+"/"+tc.requested, func(t *testing.T) {
			runtime := &listingRecorder{}
			called := false
			api := &jobinspect.Service{Runtime: runtime, Tenant: "tenant", Parent: tc.defaultParent, Authorize: func(_ context.Context, id string) error {
				require.Empty(t, runtime.requests)
				require.Equal(t, tc.want, id)
				called = true
				return nil
			}}
			_, err := api.Inspect(context.Background(), jobinspect.Request{Operation: "list", Parent: tc.requested, Limit: 7, Cursor: "cursor", Status: []jobdb.JobStatus{jobdb.JobStatusCompleted}})
			require.NoError(t, err)
			require.True(t, called)
			require.Len(t, runtime.requests, 1)
			q := runtime.requests[0]
			require.Equal(t, []string{"tenant"}, q.TenantIds)
			require.Equal(t, 7, q.PageSize)
			require.Equal(t, "cursor", q.PageToken)
			require.Equal(t, []jobdb.JobStatus{jobdb.JobStatusCompleted}, q.Statuses)
			require.False(t, q.IncludeHidden)
			if tc.want == "" {
				require.Empty(t, q.ParentJobIDs)
			} else {
				require.Equal(t, []string{tc.want}, q.ParentJobIDs)
			}
			sentinel := errors.New("denied")
			api.Authorize = func(context.Context, string) error { return sentinel }
			_, err = api.Inspect(context.Background(), jobinspect.Request{Operation: "list"})
			require.ErrorIs(t, err, sentinel)
			require.Len(t, runtime.requests, 1)
		})
	}
}

// INS-024: malformed requests fail independently of backend availability.
func TestInspectionInvalidSelectors(t *testing.T) {
	for _, q := range []jobinspect.Request{{Operation: "list", Limit: -1}, {Operation: "list", Limit: 101}, {Operation: "show", Attempt: -1}, {Operation: "show", Attempt: 2}, {Operation: "show"}} {
		_, err := (&jobinspect.Service{}).Inspect(context.Background(), q)
		require.Error(t, err)
	}
}
