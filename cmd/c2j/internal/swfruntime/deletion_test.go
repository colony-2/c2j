package swfruntime

import (
	"context"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type deletionCapture struct {
	jobdb.WorkflowRuntime
	calls int
	req   jobdb.DeleteJobsRequest
	err   error
}

func (r *deletionCapture) DeleteJobs(_ context.Context, req jobdb.DeleteJobsRequest) (jobdb.DeleteJobsResponse, error) {
	r.calls++
	r.req = req
	return jobdb.DeleteJobsResponse{MatchedCount: 12}, r.err
}

func TestChapterVisibilityForwardsDeletion(t *testing.T) {
	for _, wantErr := range []error{nil, jobdb.ErrInvalidJobDeletion, jobdb.ErrJobDeletionUnsupported, context.Canceled} {
		runtime := &deletionCapture{err: wantErr}
		wrapped := withChapterVisibility(runtime).(jobdb.JobDeletionRuntime)
		req := jobdb.DeleteJobsRequest{TenantID: "tenant", All: true, DryRun: true}
		result, err := wrapped.DeleteJobs(context.Background(), req)
		require.ErrorIs(t, err, wantErr)
		require.Equal(t, 1, runtime.calls)
		require.Equal(t, req, runtime.req)
		require.EqualValues(t, 12, result.MatchedCount)
	}
	wrapped := withChapterVisibility(struct{ jobdb.WorkflowRuntime }{}).(jobdb.JobDeletionRuntime)
	_, err := wrapped.DeleteJobs(context.Background(), jobdb.DeleteJobsRequest{TenantID: "tenant", All: true})
	require.ErrorIs(t, err, jobdb.ErrJobDeletionUnsupported)
}
