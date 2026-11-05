package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

func TestCellCreateInsideTaskUsesBroker(t *testing.T) {
	ctx, cancel := context.WithCancel(celltest.Context(context.Background()))
	defer cancel()
	rt := toy.New()
	access := permissions.NewAccess(ctx, permissions.JobDB{Cells: permissions.CellPermissions{Create: true, CellNames: []string{"reports"}}}, nil, nil, rt.CreateCell)
	current := jobcontext.Current{TenantID: "task-tenant", JobID: "parent", GoalID: "goal"}
	broker, err := childbroker.Start(permissions.WithAccess(ctx, access), childbroker.Options{Current: current, Self: func() selfinfo.Info { return selfinfo.Unknown("test") }})
	require.NoError(t, err)
	defer broker.Close()
	for key, value := range jobcontext.EnvForCurrent(current) {
		t.Setenv(key, value)
	}
	for key, value := range broker.Env() {
		t.Setenv(key, value)
	}
	// An ambient destination must not redirect a task's creation authority.
	t.Setenv("C2J_JOBDB", "https://invalid.example/other-tenant")
	run := func(name string, extra ...string) (jobdb.Cell, error) {
		cmd := newCellCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"create", name, "--repo", "https://example.com/reports.git", "--request-id", "stable-create", "--json"}, extra...))
		err := cmd.Execute()
		var cell jobdb.Cell
		if err == nil {
			err = json.Unmarshal(out.Bytes(), &cell)
		}
		return cell, err
	}
	first, err := run("reports")
	require.NoError(t, err)
	require.Equal(t, "task-tenant", first.TenantID)
	retry, err := run("reports")
	require.NoError(t, err)
	require.Equal(t, first.ID, retry.ID)
	_, err = run("denied")
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
	_, err = run("reports", "--jobdb", "embed:///")
	require.ErrorContains(t, err, "cannot retarget")
	t.Setenv(jobcontext.ChildJobTokenEnv, "bad-token")
	_, err = run("reports")
	require.ErrorContains(t, err, "401")
	for key := range broker.Env() {
		t.Setenv(key, "")
	}
	_, err = run("reports")
	require.ErrorContains(t, err, "requires a task broker")
}
