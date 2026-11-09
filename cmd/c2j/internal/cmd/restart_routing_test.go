package cmd

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/stretchr/testify/require"
)

func TestRestartRoutingNeverBypassesTaskHelper(t *testing.T) {
	current := jobcontext.Current{TenantID: "tenant", JobID: "current", GoalID: "goal"}
	for k, v := range jobcontext.EnvForCurrent(current) {
		t.Setenv(k, v)
	}
	p := &restartProbe{}
	server, err := childbroker.Start(jobrestart.WithAPI(context.Background(), p), childbroker.Options{Current: current, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer server.Close()
	for k, v := range server.Env() {
		t.Setenv(k, v)
	}
	t.Setenv("C2J_JOBDB", "invalid://ambient")
	out, err := executeRestartTest(newJobCmd(), "restart-points", "self", "--json", "--jobdb", "invalid://explicit")
	require.NoError(t, err)
	require.Contains(t, out, `"project_id":"tenant"`)
	out, err = executeRestartTest(newJobCmd(), "restart-context", "self", "--scope", "opaque", "--jobdb", "invalid://explicit")
	require.NoError(t, err)
	require.Contains(t, out, `"actual_name":"full"`)
	_, err = executeRestartTest(newRestartCmd(), "self", "--scope", "opaque", "--request-id", "key", "--jobdb", "invalid://explicit")
	require.NoError(t, err)
	require.Len(t, p.requests, 3)
	for _, q := range p.requests {
		require.Equal(t, "current", q.JobID)
	}
	for k := range server.Env() {
		t.Setenv(k, "")
	}
	_, _, _, err = openRestart(context.Background(), "invalid://explicit")
	require.ErrorContains(t, err, "task restart helper unavailable")
	// Older helpers may serve inspection but have no restart endpoint capability.
	old, err := childbroker.Start(context.Background(), childbroker.Options{Current: current, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer old.Close()
	for k, v := range old.Env() {
		t.Setenv(k, v)
	}
	_, err = executeRestartTest(newJobCmd(), "restart-points", "self", "--jobdb", "invalid://explicit")
	require.ErrorContains(t, err, "capability unavailable")
}
