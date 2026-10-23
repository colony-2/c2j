package permissions

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

type creatorFunc func(context.Context, jobdb.ExecutionLease, jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error)

func (f creatorFunc) CreateDelegation(ctx context.Context, p jobdb.ExecutionLease, req jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
	return f(ctx, p, req)
}

type issuedLease struct {
	jobdb.RenewableExecutionLease
	renewErr error
	renewals int
}

func (l *issuedLease) KeepAlive(context.Context) error { l.renewals++; return l.renewErr }

func TestCentralIssuerPreservesGrantAndEarliestDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	child := &issuedLease{}
	calls := 0
	issuer := JobDBIssuer{Creator: creatorFunc(func(_ context.Context, _ jobdb.ExecutionLease, req jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
		calls++
		require.Equal(t, deadline, *req.NotAfter)
		require.Equal(t, []string{"model"}, req.Permissions.Secrets)
		require.Nil(t, req.Permissions.Read)
		require.Nil(t, req.Permissions.Write)
		require.True(t, req.Permissions.Submit.JobTypes.Any)
		require.True(t, req.Permissions.Submit.JobIDs.Any)
		return child, nil
	})}
	grant := JobDB{Secrets: SecretPermissions{Resolve: []string{"model"}}, Jobs: JobPermissions{Submit: true}}
	got, err := issuer.IssueRestrictedLease(ctx, &issuedLease{}, grant, deadline.Add(time.Hour))
	require.NoError(t, err)
	require.Same(t, child, got)
	require.Equal(t, 0, child.renewals, "issuance is local and must not call renewal")
	_, err = issuer.IssueRestrictedLease(context.Background(), &issuedLease{}, grant, time.Time{})
	require.ErrorContains(t, err, "finite")
	require.Equal(t, 1, calls)

}

func TestLocalAttenuationAndRenewalRespectHardDeadline(t *testing.T) {
	ctx := context.Background()
	base := toy.New()
	server := httptest.NewServer(remote.NewServer(base))
	defer server.Close()
	client, err := remote.New(server.URL, server.Client())
	require.NoError(t, err)
	job, err := client.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobID: "parent", JobType: "test", Data: jobdb.NewTaskDataOrPanic(map[string]any{})}})
	require.NoError(t, err)
	owner, err := client.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: job.JobKey, WorkerID: "test", Routes: []jobdb.Route{{JobType: "test"}}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.NotNil(t, owner)
	issuer := JobDBIssuer{Creator: client}
	deadline := time.Now().UTC().Add(time.Second)
	child, err := issuer.IssueRestrictedLease(ctx, owner, JobDB{Jobs: JobPermissions{Submit: true}}, deadline)
	require.NoError(t, err)
	info := child.(interface{ Delegation() *jobdb.DelegationInfo }).Delegation()
	require.Equal(t, deadline, *info.NotAfter)
	renewed, err := child.(jobdb.RenewableExecutionLease).Renew(ctx)
	require.NoError(t, err)
	require.Equal(t, deadline, *renewed.(interface{ Delegation() *jobdb.DelegationInfo }).Delegation().NotAfter)
	// Further local attenuation cannot widen permissions or the parent's deadline.
	narrow, err := issuer.IssueRestrictedLease(ctx, child, JobDB{}, deadline.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, deadline, *narrow.(interface{ Delegation() *jobdb.DelegationInfo }).Delegation().NotAfter)
	_, err = issuer.IssueRestrictedLease(ctx, narrow, JobDB{Jobs: JobPermissions{Submit: true}}, deadline)
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
	jobReq := jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobType: "child", Data: jobdb.NewTaskDataOrPanic(nil)}}
	_, err = narrow.SubmitJob(ctx, jobReq)
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
	_, err = child.SubmitJob(ctx, jobReq)
	require.NoError(t, err)
	time.Sleep(time.Until(deadline) + 20*time.Millisecond)
	require.Error(t, child.KeepAlive(ctx))
	_, err = child.SubmitJob(ctx, jobReq)
	require.Error(t, err)
	// Child expiration and refresh do not terminate or renew the owner.
	require.NoError(t, owner.KeepAlive(ctx))
}
