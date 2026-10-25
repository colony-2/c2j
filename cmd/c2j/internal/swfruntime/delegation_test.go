package swfruntime

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type delegationCapture struct {
	jobdb.WorkflowRuntime
	parent  jobdb.ExecutionLease
	request jobdb.CreateDelegationRequest
	calls   int
}

func (r *delegationCapture) CreateDelegation(_ context.Context, parent jobdb.ExecutionLease, request jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
	r.calls++
	r.parent, r.request = parent, request
	return nil, jobdb.ErrLeasePermissionDenied
}
func TestChapterVisibilityPreservesDelegationCapability(t *testing.T) {
	base := &delegationCapture{}
	wrapped := withChapterVisibility(base)
	delegator, ok := wrapped.(jobdb.LeaseDelegator)
	require.True(t, ok)
	deadline := time.Now().Add(time.Minute)
	req := jobdb.CreateDelegationRequest{NotAfter: &deadline, Permissions: jobdb.DelegationPermissions{Secrets: []string{"ro"}}}
	parent := struct{ jobdb.ExecutionLease }{}
	_, err := delegator.CreateDelegation(context.Background(), parent, req)
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
	require.Equal(t, 1, base.calls)
	require.Equal(t, parent, base.parent)
	require.Equal(t, req, base.request)
	_, ok = withChapterVisibility(struct{ jobdb.WorkflowRuntime }{}).(jobdb.LeaseDelegator)
	require.False(t, ok, "embedded runtimes must not advertise unsupported delegation")
}
