package permissions

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type creatorFunc func(context.Context, jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error)

func (f creatorFunc) CreateDelegation(ctx context.Context, req jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
	return f(ctx, req)
}

type issuedLease struct {
	jobdb.RenewableExecutionLease
	info     *jobdb.DelegationInfo
	renewErr error
	renewals int
}

func (l *issuedLease) Delegation() *jobdb.DelegationInfo { return l.info }

func (l *issuedLease) KeepAlive(context.Context) error { l.renewals++; return l.renewErr }

func TestCentralIssuerUsesTaskBoundsNotCallTimeout(t *testing.T) {
	deadline := time.Now().UTC().Add(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	child := &issuedLease{info: &jobdb.DelegationInfo{NotAfter: &deadline}}
	issuer := JobDBIssuer{}
	for _, limit := range []time.Time{{}, deadline} {
		task := creatorFunc(func(call context.Context, req jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
			require.Same(t, ctx, call)
			if limit.IsZero() {
				require.Nil(t, req.NotAfter, "JobDB supplies the execution bounds")
			} else {
				require.Equal(t, limit, *req.NotAfter)
			}
			require.Equal(t, []string{"model"}, req.Permissions.Secrets)
			require.Nil(t, req.Permissions.Read)
			require.Nil(t, req.Permissions.Write)
			require.True(t, req.Permissions.Submit.JobTypes.Any)
			require.True(t, req.Permissions.Submit.JobIDs.Any)
			return child, nil
		})
		grant := JobDB{Secrets: SecretPermissions{Resolve: []string{"model"}}, Jobs: JobPermissions{Submit: true}}
		got, err := issuer.IssueRestrictedLease(ctx, task, grant, limit)
		require.NoError(t, err)
		require.Same(t, child, got)
	}
	require.Zero(t, child.renewals, "issuance is local and must not call renewal")
}

func TestCentralIssuerRejectsMissingTaskCapability(t *testing.T) {
	issuer := JobDBIssuer{}
	_, err := issuer.IssueRestrictedLease(context.Background(), jobworkflow.TaskContext{}, JobDB{}, time.Time{})
	require.ErrorIs(t, err, jobdb.ErrLeaseDelegationUnsupported)
}

func TestCentralIssuerAcceptsParentBoundLeaseAndRejectsInvalidMetadata(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	for _, tc := range []struct {
		name string
		info *jobdb.DelegationInfo
		want string
	}{
		{name: "parent bound", info: &jobdb.DelegationInfo{}},
		{name: "missing metadata", want: "missing delegation metadata"},
		{name: "expired", info: &jobdb.DelegationInfo{NotAfter: &past}, want: "deadline has expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := &issuedLease{info: tc.info}
			got, err := (JobDBIssuer{}).IssueRestrictedLease(context.Background(), creatorFunc(func(_ context.Context, req jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
				require.Nil(t, req.NotAfter)
				return child, nil
			}), JobDB{}, time.Time{})
			if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.Same(t, child, got)
			}
		})
	}
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
	deadline := time.Now().UTC().Add(time.Second)
	child, err := client.CreateDelegation(ctx, owner, jobdb.CreateDelegationRequest{Permissions: jobdb.DelegationPermissions{Submit: &jobdb.DelegationSubmitPermission{JobTypes: jobdb.DelegationNames{Any: true}, JobIDs: jobdb.DelegationNames{Any: true}}}, NotAfter: &deadline})
	require.NoError(t, err)
	info := child.(interface{ Delegation() *jobdb.DelegationInfo }).Delegation()
	require.Equal(t, deadline, *info.NotAfter)
	renewed, err := child.Renew(ctx)
	require.NoError(t, err)
	require.Equal(t, deadline, *renewed.(interface{ Delegation() *jobdb.DelegationInfo }).Delegation().NotAfter)
	// Further local attenuation cannot widen permissions or the parent's deadline.
	narrow, err := client.CreateDelegation(ctx, child, jobdb.CreateDelegationRequest{})
	require.NoError(t, err)
	require.Equal(t, deadline, *narrow.(interface{ Delegation() *jobdb.DelegationInfo }).Delegation().NotAfter)
	_, err = client.CreateDelegation(ctx, narrow, jobdb.CreateDelegationRequest{Permissions: info.Permissions, NotAfter: &deadline})
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
