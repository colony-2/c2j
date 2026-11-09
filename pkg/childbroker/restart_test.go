package childbroker

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/stretchr/testify/require"
)

type restartProbe struct{ calls atomic.Int32 }

func (p *restartProbe) Restart(_ context.Context, q jobrestart.Request) (*jobrestart.Response, error) {
	p.calls.Add(1)
	return &jobrestart.Response{Points: &jobrestart.Points{ProjectID: "tenant", JobID: q.JobID, RestartScopes: []jobrestart.RestartScope{}}}, nil
}
func TestRestartHelperAuthenticationAndCancellation(t *testing.T) {
	p := &restartProbe{}
	server, err := Start(jobrestart.WithAPI(context.Background(), p), Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "current", GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer server.Close()
	broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, err)
	for _, bad := range []string{"token", "session"} {
		b := broker
		if bad == "token" {
			b.Token = "bad"
		} else {
			b.SessionID = "bad"
		}
		_, err := (JobClient{Broker: b}).Restart(context.Background(), jobrestart.Request{Operation: "points", JobID: "job"})
		require.ErrorContains(t, err, "401")
	}
	require.Zero(t, p.calls.Load())
	result, err := (JobClient{Broker: broker}).Restart(context.Background(), jobrestart.Request{Operation: "points", JobID: "job"})
	require.NoError(t, err)
	require.Equal(t, "job", result.Points.JobID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (JobClient{Broker: broker}).Restart(ctx, jobrestart.Request{Operation: "points", JobID: "job"})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int32(1), p.calls.Load())
}
