package permissions

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestGroupedAccessUsesGrantAndScope(t *testing.T) {
	for _, tc := range []struct {
		name    string
		grant   JobDB
		cancel  bool
		allowed bool
	}{
		{name: "denied"},
		{name: "wrong cell", grant: JobDB{Jobs: JobPermissions{Submit: true, CellNames: []string{"other"}}}},
		{name: "cancelled", grant: JobDB{Jobs: JobPermissions{Submit: true}}, cancel: true},
		{name: "allowed", grant: JobDB{Jobs: JobPermissions{Submit: true, CellNames: []string{"test"}}}, allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			a := NewAccess(scope, tc.grant, nil, nil).WithAppend(func(context.Context, jobdb.StartOrAppendJob) (jobref.Ref, error) {
				calls++
				return jobref.Ref{JobID: "shared"}, nil
			})
			if tc.cancel {
				cancel()
			}
			_, err := a.StartOrAppend(context.Background(), jobdb.StartOrAppendJob{Job: jobdb.SubmitJob{Cell: "test"}})
			if tc.allowed {
				require.NoError(t, err)
				require.Equal(t, 1, calls)
			} else {
				require.Error(t, err)
				require.Zero(t, calls)
			}
		})
	}
	a := NewAccess(context.Background(), JobDB{Jobs: JobPermissions{Submit: true}}, nil, nil)
	_, err := a.StartOrAppend(context.Background(), jobdb.StartOrAppendJob{Job: jobdb.SubmitJob{Cell: "test"}})
	require.ErrorContains(t, err, "unavailable")
}
