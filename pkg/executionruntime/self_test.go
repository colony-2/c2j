package executionruntime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type selfLease struct {
	jobdb.ExecutionLease
	key    jobdb.JobKey
	expiry time.Time
	bounds *jobdb.DelegationInfo
}

func (l *selfLease) Job() jobdb.JobHandle              { return jobdb.JobHandle{JobKey: l.key} }
func (*selfLease) LeaseID() string                     { return "lease-123" }
func (*selfLease) Route() jobdb.Route                  { return jobdb.Route{JobType: "recipe"} }
func (l *selfLease) LeaseExpiry() time.Time            { return l.expiry }
func (l *selfLease) Delegation() *jobdb.DelegationInfo { return l.bounds }
func (*selfLease) ResolveSecrets(context.Context, []string) (jobdb.SecretResolveResult, error) {
	return jobdb.SecretResolveResult{}, nil
}

type selfIssuer struct {
	calls int
	child *selfLease
}

func (s *selfIssuer) IssueRestrictedLease(_ context.Context, _ permissions.TaskDelegator, grant permissions.JobDB, deadline time.Time) (jobdb.ExecutionLease, error) {
	s.calls++
	s.child.bounds = &jobdb.DelegationInfo{ID: "delegation-123", NotAfter: &deadline, Permissions: jobdb.DelegationPermissions{Secrets: grant.Secrets.Resolve}}
	return s.child, nil
}
func TestSelfInspectionTracksLifetimeWithoutMintingAuthority(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	backing := &selfLease{key: key, expiry: time.Now().Add(time.Minute)}
	issuer := &selfIssuer{child: &selfLease{key: key}}
	rt := &Runtime{leases: map[jobdb.JobKey]*lease{}, Issuer: issuer}
	original := rt.wrapLease(backing, &leaseState{}).(*lease)
	tc := wf.TaskContext{JobKey: key, InvocationID: "logical-task", AttemptNumber: 2, Step: 47}
	ctx, cancel := context.WithCancel(celltest.Context(context.Background()))
	defer cancel()
	grant := permissions.JobDB{Jobs: permissions.JobPermissions{Submit: true}, Secrets: permissions.SecretPermissions{Resolve: []string{"model"}}}
	ceiling := permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"model"}}}
	access, local, stop, err := rt.restrictedAccess(ctx, tc, original, grant, nil, permissions.State{Version: 1, ChildGrant: &ceiling, ChildPolicy: json.RawMessage(`{"revision":"1"}`)})
	require.NoError(t, err)
	defer stop()
	inspect := selfinfo.FromContext(local)
	require.NotNil(t, inspect)
	first := inspect()
	require.Equal(t, "logical-task", selfinfo.Value(first.Execution.TaskID))
	require.Equal(t, 2, *first.Execution.AttemptNumber)
	require.Equal(t, int64(47), *first.Execution.ResultOrdinal)
	require.Equal(t, "active", first.Lease.Status)
	require.Equal(t, "last_known", first.Lease.Validation)
	require.Nil(t, first.Lease.ValidatedAt)
	require.Equal(t, "not_issued", first.Lease.DelegationStatus)
	require.Equal(t, "known", first.Permissions.ChildCeiling.Status)
	require.False(t, first.Permissions.ChildCeiling.JobDB.Jobs.Submit)
	require.True(t, first.Permissions.JobDB.Jobs.Submit)
	require.Zero(t, issuer.calls, "inspection must not issue delegation")
	deadline := time.Now().Add(20 * time.Second)
	limit := access.LimitDeadline(deadline)
	defer limit()
	require.WithinDuration(t, deadline, *inspect().Lease.OperationDeadline, time.Millisecond)
	_, err = access.Resolve(celltest.Context(context.Background()), []string{"model"})
	require.NoError(t, err)
	require.Equal(t, 1, issuer.calls)
	after := inspect()
	after.SetCapabilities(true, true, true)
	require.Equal(t, "issued", after.Lease.DelegationStatus)
	require.Nil(t, after.Lease.Delegation.Permissions.Submit)
	require.Equal(t, "granted", after.Capabilities["jobs.submit"].Permission, "controller-mediated submit must remain discoverable")
	refreshed := &selfLease{key: key, expiry: time.Now().Add(2 * time.Minute)}
	rt.wrapLease(refreshed, original.leaseState)
	require.True(t, inspect().Lease.ExpiresAt.After(*first.Lease.ExpiresAt))
	cancel()
	require.Equal(t, "cancelled", inspect().Lease.Status)
	require.Equal(t, 1, issuer.calls)
}
func TestSelfInspectionLostAndRestrictedParent(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	backing := &selfLease{key: key, bounds: &jobdb.DelegationInfo{Permissions: jobdb.DelegationPermissions{Secrets: []string{"allowed"}}}}
	rt := &Runtime{leases: map[jobdb.JobKey]*lease{}}
	original := rt.wrapLease(backing, &leaseState{}).(*lease)
	grant := permissions.JobDB{Jobs: permissions.JobPermissions{Submit: true}, Secrets: permissions.SecretPermissions{Resolve: []string{"allowed", "other"}}}
	access := permissions.NewAccess(celltest.Context(context.Background()), grant, nil, nil)
	inspect := rt.selfProvider(wf.TaskContext{JobKey: key}, original, access, nil, func() *jobdb.DelegationInfo { return nil })
	info := inspect()
	require.Nil(t, info.Execution.AttemptNumber)
	require.False(t, info.Permissions.JobDB.Jobs.Submit)
	require.Equal(t, []string{"allowed"}, info.Permissions.JobDB.Secrets.Resolve)
	original.forget()
	require.Equal(t, "lost", inspect().Lease.Status)
}

func TestSelfReportsGrantEditsUntilReplacement(t *testing.T) {
	disabled := false
	policy, err := (permissions.State{}).Enter(&permissions.Spec{Inherit: true, Overrides: &permissions.JobDBOverrides{Jobs: permissions.JobOverrides{Submit: &disabled}}}, false)
	require.NoError(t, err)
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	rt := &Runtime{leases: map[jobdb.JobKey]*lease{}}
	original := rt.wrapLease(&selfLease{key: key}, &leaseState{}).(*lease)
	for _, replace := range []bool{false, true} {
		if replace {
			policy, err = policy.Enter(&permissions.Spec{JobDB: &permissions.JobDB{Jobs: permissions.JobPermissions{Submit: true}}}, false)
			require.NoError(t, err)
		}
		grant, err := policy.Resolve([]string{"ro"})
		require.NoError(t, err)
		access := permissions.NewAccess(celltest.Context(context.Background()), grant, nil, nil)
		info := rt.selfProvider(wf.TaskContext{JobKey: key}, original, access, []permissions.State{policy}, func() *jobdb.DelegationInfo { return nil })()
		info.SetCapabilities(true, true, true)
		if replace {
			require.Empty(t, info.Permissions.InheritedDenials)
			require.Empty(t, info.Capabilities["jobs.submit"].Restrictions)
			require.Equal(t, "granted", info.Capabilities["jobs.submit"].Permission)
		} else {
			require.Equal(t, []string{"perms.jobdb.jobs.submit"}, info.Permissions.InheritedDenials)
			for _, name := range []string{"jobs.submit", "jobs.submit_from_workspace", "jobs.submit_from_workspace_now"} {
				require.Equal(t, info.Permissions.InheritedDenials, info.Capabilities[name].Restrictions)
				require.Equal(t, "denied", info.Capabilities[name].Permission)
			}
			require.Empty(t, info.Capabilities["secrets.resolve"].Restrictions)
		}
	}
}

// A read-only snapshot must stay responsive while a permitted operation is
// waiting for its first delegated credential.
type blockingSelfIssuer struct {
	started chan struct{}
	release chan struct{}
	child   *selfLease
}

func (s *blockingSelfIssuer) IssueRestrictedLease(ctx context.Context, _ permissions.TaskDelegator, _ permissions.JobDB, _ time.Time) (jobdb.ExecutionLease, error) {
	close(s.started)
	select {
	case <-s.release:
		return s.child, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestSelfInspectionDoesNotWaitForDelegation(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	issuer := &blockingSelfIssuer{started: make(chan struct{}), release: make(chan struct{}), child: &selfLease{key: key}}
	rt := &Runtime{leases: map[jobdb.JobKey]*lease{}, Issuer: issuer}
	original := rt.wrapLease(&selfLease{key: key}, &leaseState{}).(*lease)
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 5*time.Second)
	defer cancel()
	access, local, stop, err := rt.restrictedAccess(ctx, wf.TaskContext{JobKey: key}, original, permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"model"}}}, nil)
	require.NoError(t, err)
	defer stop()
	done := make(chan error, 1)
	go func() { _, err := access.Resolve(ctx, []string{"model"}); done <- err }()
	select {
	case <-issuer.started:
	case <-ctx.Done():
		t.Fatal("issuer did not start")
	}
	observed := make(chan selfinfo.Info, 1)
	go func() { observed <- selfinfo.FromContext(local)() }()
	select {
	case report := <-observed:
		require.Nil(t, report.Lease.Delegation)
	case <-time.After(time.Second):
		t.Fatal("inspection blocked on credential issuance")
	}
	close(issuer.release)
	require.NoError(t, <-done)
}

func TestSelfInspectionExpiredLifetime(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	for _, operation := range []bool{false, true} {
		t.Run(map[bool]string{false: "lease", true: "operation"}[operation], func(t *testing.T) {
			expiry := time.Now().Add(-time.Minute)
			if operation {
				expiry = time.Now().Add(time.Minute)
			}
			rt := &Runtime{leases: map[jobdb.JobKey]*lease{}}
			original := rt.wrapLease(&selfLease{key: key, expiry: expiry}, &leaseState{}).(*lease)
			access := permissions.NewAccess(celltest.Context(context.Background()), permissions.JobDB{Jobs: permissions.JobPermissions{Submit: true}}, nil, nil)
			if operation {
				cancel := access.LimitDeadline(time.Now().Add(-time.Second))
				defer cancel()
			}
			report := rt.selfProvider(wf.TaskContext{JobKey: key}, original, access, nil, func() *jobdb.DelegationInfo { return nil })()
			report.SetCapabilities(true, true, true)
			require.Equal(t, "expired", report.Lease.Status)
			require.Equal(t, "granted", report.Capabilities["jobs.submit"].Permission)
			require.Equal(t, "unavailable", report.Capabilities["jobs.submit"].Availability)
			if operation {
				require.Equal(t, "operation_deadline_exceeded", report.Lease.ReasonCode)
			}
		})
	}
}
