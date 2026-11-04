package permissions

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCellSubmissionAttenuation(t *testing.T) {
	var spec Spec
	require.NoError(t, yaml.Unmarshal([]byte("jobdb:\n  jobs: {submit: true, cellNames: [payments, reports]}\n"), &spec))
	a := *spec.JobDB
	b := JobDB{Jobs: JobPermissions{Submit: true, CellNames: []string{"reports", "audit"}}}
	narrowed := Intersect(a, b)
	require.Equal(t, []string{"reports"}, narrowed.Jobs.CellNames)
	require.True(t, narrowed.Jobs.Submit)
	require.False(t, Intersect(a, JobDB{Jobs: JobPermissions{Submit: true, CellNames: []string{"audit"}}}).Jobs.Submit)
	require.Equal(t, a, Intersect(a, JobDB{Jobs: JobPermissions{Submit: true}}))
	_, err := (State{Limit: &a}).Enter(&Spec{JobDB: &b}, false)
	require.Error(t, err, "a nested grant must not widen the enclosing cells")
	for _, jobs := range []JobPermissions{{CellNames: []string{"payments"}}, {Submit: true, CellNames: []string{}}, {Submit: true, CellNames: []string{"payments", "payments"}}, {Submit: true, CellNames: []string{"example.com/payments"}}} {
		require.Error(t, (JobDB{Jobs: jobs}).Validate())
	}
	called := false
	access := NewAccess(context.Background(), narrowed, nil, func(context.Context, jobdb.SubmitJob) (jobref.Ref, error) { called = true; return jobref.Ref{}, nil })
	_, err = access.Submit(context.Background(), jobdb.SubmitJob{Cell: "payments"})
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
	require.False(t, called)
	_, err = access.Submit(context.Background(), jobdb.SubmitJob{Cell: "reports"})
	require.NoError(t, err)
	require.True(t, called)
	child := &issuedLease{info: &jobdb.DelegationInfo{}}
	_, err = (JobDBIssuer{}).IssueRestrictedLease(context.Background(), creatorFunc(func(_ context.Context, req jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
		require.Equal(t, []string{"reports"}, req.Permissions.Submit.CellNames.Names)
		return child, nil
	}), narrowed, time.Time{})
	require.NoError(t, err)
}
