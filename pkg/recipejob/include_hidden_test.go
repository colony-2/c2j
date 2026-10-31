package recipejob

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type hiddenLister struct {
	calls int
	req   jobdb.ListJobsRequest
}

func (l *hiddenLister) ListJobs(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	l.calls++
	l.req = req
	return jobdb.ListJobsResponse{NextPageToken: "next"}, nil
}

type hiddenWorkflowLister struct{ hiddenLister }

func (l *hiddenWorkflowLister) ListJobs(_ context.Context, req jobdb.ListJobsRequest) ([]workflowctl.JobItem, string, error) {
	l.calls++
	l.req = req
	return nil, "next", nil
}
func TestIncludeHiddenRecipeRequests(t *testing.T) {
	ctx := context.Background()
	for _, include := range []bool{false, true} {
		start, end := time.Unix(1, 0), time.Unix(2, 0)
		r := ListRecipeJobsRequest{TenantID: "tenant", RepositorySource: "github.com/acme/app", IncludeHidden: include, GoalIDs: []string{"goal"}, JobIDs: []string{"job"}, Statuses: []jobdb.JobStatus{jobdb.JobStatusCancelled}, Stores: []jobdb.JobStore{jobdb.JobStoreArchived}, CreatedAfter: &start, CreatedBefore: &end, PageSize: 2, PageToken: "cursor"}
		req, err := BuildListJobsRequest(r)
		require.NoError(t, err)
		require.Equal(t, include, req.IncludeHidden)
		require.Equal(t, r.GoalIDs, req.GoalIDs)
		require.Equal(t, r.Statuses, req.Statuses)
		require.Equal(t, r.Stores, req.Stores)
		require.Equal(t, r.CreatedAfter, req.CreatedAfter)
		require.Equal(t, r.CreatedBefore, req.CreatedBefore)
		require.Equal(t, 2, req.PageSize)
		require.Equal(t, "cursor", req.PageToken)
		require.Equal(t, []jobdb.JobKey{{TenantId: "tenant", JobId: "job"}}, req.JobKeys)
		l := &hiddenLister{}
		page, err := ListRecipeJobs(ctx, l, r)
		require.NoError(t, err)
		require.Equal(t, req, l.req)
		require.Equal(t, "next", page.NextPageToken)
		require.Equal(t, 1, l.calls)
		child := ListChildRecipeJobsRequest{TenantID: "tenant", ParentTenantID: "tenant", ParentJobID: "parent", ParentInvocationHash: "hash", IncludeHidden: include, Statuses: r.Statuses, Stores: r.Stores, PageSize: 2, PageToken: "cursor"}
		childReq, err := BuildListChildJobsRequest(child)
		require.NoError(t, err)
		require.Equal(t, include, childReq.IncludeHidden)
		require.Equal(t, []string{"parent"}, childReq.ParentJobIDs)
		require.Equal(t, child.Statuses, childReq.Statuses)
		require.Equal(t, child.Stores, childReq.Stores)
		predicates, err := jobdb.MetadataPredicates(childReq.MetadataFilter)
		require.NoError(t, err)
		require.Equal(t, []any{"hash"}, predicates[0].Values)
		l = &hiddenLister{}
		page, err = ListChildRecipeJobs(ctx, l, child)
		require.NoError(t, err)
		require.Equal(t, childReq, l.req)
		require.Equal(t, 1, l.calls)
		require.Equal(t, "next", page.NextPageToken)
		w := &hiddenWorkflowLister{}
		page, err = ListChildRecipeJobsFromWorkflow(ctx, w, child)
		require.NoError(t, err)
		require.Equal(t, childReq, w.req)
		require.Equal(t, 1, w.calls)
		require.Equal(t, "next", page.NextPageToken)
	}
}
func TestIncludeHiddenDoesNotBypassChildValidation(t *testing.T) {
	for _, r := range []ListChildRecipeJobsRequest{
		{TenantID: "tenant", ParentTenantID: "tenant", IncludeHidden: true},
		{TenantID: "tenant", ParentTenantID: "other", ParentJobID: "parent", AllParentInvocations: true, IncludeHidden: true},
		{TenantID: "tenant", ParentTenantID: "tenant", ParentJobID: "parent", IncludeHidden: true},
	} {
		l := &hiddenLister{}
		_, err := ListChildRecipeJobs(context.Background(), l, r)
		require.Error(t, err)
		require.Zero(t, l.calls)
		w := &hiddenWorkflowLister{}
		_, err = ListChildRecipeJobsFromWorkflow(context.Background(), w, r)
		require.Error(t, err)
		require.Zero(t, w.calls)
	}
}

func TestRawIncludeHiddenPassThrough(t *testing.T) {
	for _, include := range []bool{false, true} {
		l := &hiddenLister{}
		req := jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, IncludeHidden: include, PageToken: "cursor"}
		page, err := ListExecutionJobs(context.Background(), l, req, nil)
		require.NoError(t, err)
		require.Equal(t, req, l.req)
		require.Equal(t, 1, l.calls)
		require.Equal(t, "next", page.NextPageToken)
	}
}
