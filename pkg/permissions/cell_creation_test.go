package permissions

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCellCreationGrantsAndCeilings(t *testing.T) {
	defaults, err := (State{Version: Version}).Resolve(nil)
	require.NoError(t, err)
	require.False(t, defaults.Cells.Create)
	a := *parse(t, "jobdb: {cells: {create: true, cellNames: [payments, reports]}}").JobDB
	b := *parse(t, "jobdb: {cells: {create: true, cellNames: [reports, audit]}}").JobDB
	all := JobDB{Cells: CellPermissions{Create: true}}
	require.Equal(t, a, Intersect(a, all))
	require.Equal(t, []string{"reports"}, Intersect(a, b).Cells.CellNames)
	require.True(t, Intersect(a, b).Cells.Create)
	require.False(t, Intersect(a, JobDB{Cells: CellPermissions{Create: true, CellNames: []string{"audit"}}}).Cells.Create)
	require.False(t, Intersect(a, JobDB{}).Cells.Create)
	_, err = (State{Limit: &a}).Enter(&Spec{JobDB: &b}, false)
	require.ErrorContains(t, err, "exceed")
	_, err = (State{Limit: &a}).Enter(&Spec{JobDB: &all}, false)
	require.ErrorContains(t, err, "exceed")
	state, err := (State{}).Enter(parse(t, "jobdb: {cells: {create: true}}"), false)
	require.NoError(t, err)
	state, err = state.EnterInclude(parse(t, "jobdb: {cells: {create: true, cellNames: [reports]}}"), true, false)
	require.NoError(t, err)
	grant, err := state.Resolve(nil)
	require.NoError(t, err)
	require.False(t, grant.Cells.Create, "include authority is a ceiling, not an inherited grant")
	state, err = state.Enter(parse(t, "jobdb: {cells: {create: true, cellNames: [reports]}}"), false)
	require.NoError(t, err)
	grant, err = state.Resolve(nil)
	require.NoError(t, err)
	require.True(t, grant.Cells.Create)
	require.False(t, grant.Jobs.Submit, "creation does not grant submission")
}

func TestCellCreationSyntaxAndHistoricalEncoding(t *testing.T) {
	for _, raw := range []string{
		"jobdb: {cells: {create: 'true'}}", "jobdb: {cells: {create: null}}",
		"jobdb: {cells: {update: true}}", "jobdb: {cells: {cellNames: [x]}}",
		"jobdb: {cells: {create: true, cellNames: []}}",
		"jobdb: {cells: {create: true, cellNames: [x, x]}}",
		"jobdb: {cells: {create: true, cellNames: ['*']}}",
		"jobdb: {cells: {create: true, cellNames: ['${{ inputs.cell }}']}}",
	} {
		var spec Spec
		require.Error(t, yaml.Unmarshal([]byte(raw), &spec), raw)
	}
	raw := `{"version":1,"grant":{"secrets":{},"jobs":{"submit":true}}}`
	var state State
	require.NoError(t, json.Unmarshal([]byte(raw), &state))
	encoded, err := json.Marshal(state)
	require.NoError(t, err)
	require.JSONEq(t, raw, string(encoded), "omitted creation grants must not change historical task inputs")
	spec := parse(t, "jobdb: {cells: {create: true, cellNames: [root]}}")
	encoded, err = yaml.Marshal(spec)
	require.NoError(t, err)
	var roundTrip Spec
	require.NoError(t, yaml.Unmarshal(encoded, &roundTrip))
	require.Equal(t, spec, &roundTrip)
}

func TestCellCreationAccessAndDelegation(t *testing.T) {
	grant := JobDB{Cells: CellPermissions{Create: true, CellNames: []string{"payments"}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	access := NewAccess(ctx, grant, nil, nil, func(_ context.Context, req jobdb.CreateCellRequest) (jobdb.Cell, error) {
		calls++
		return jobdb.Cell{Name: req.Name}, nil
	})
	_, err := access.CreateCell(ctx, jobdb.CreateCellRequest{Name: "audit"})
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
	require.Zero(t, calls)
	_, err = access.CreateCell(ctx, jobdb.CreateCellRequest{Name: "payments"})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	cancel()
	_, err = access.CreateCell(context.Background(), jobdb.CreateCellRequest{Name: "payments"})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
	for _, g := range []JobDB{grant, {Cells: CellPermissions{Create: true}}, {}} {
		_, err = (JobDBIssuer{}).IssueRestrictedLease(context.Background(), creatorFunc(func(_ context.Context, req jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
			require.Nil(t, req.Permissions.Submit)
			if !g.Cells.Create {
				require.Nil(t, req.Permissions.CreateCells)
			} else {
				require.Equal(t, g.Cells.CellNames == nil, req.Permissions.CreateCells.Any)
				require.Equal(t, g.Cells.CellNames, req.Permissions.CreateCells.Names)
			}
			return &issuedLease{info: &jobdb.DelegationInfo{}}, nil
		}), g, time.Time{})
		require.NoError(t, err)
	}
}
