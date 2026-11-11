package childbroker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// JP-14, JP-16, JP-20: helper validation is independent of the installed API.
func TestRestartHelperPatchIngress(t *testing.T) {
	p := &restartProbe{}
	server, err := Start(jobrestart.WithAPI(context.Background(), p), Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "current", GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer server.Close()
	broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, err)
	client := JobClient{Broker: broker}
	for _, raw := range []string{
		`{"job":{"git":{"author":"legacy"}}}`, `null`,
		`[{"op":"copy","from":"/context/git/hash","path":"/sequence/s/outputs/x"}]`,
		`[{"op":"add","path":"/sequence/s/outputs/x"}]`,
	} {
		var q map[string]any
		require.NoError(t, json.Unmarshal([]byte(`{"operation":"submit","job_id":"job","selector":{"scope":"s"},"request_id":"accepted-legacy-retry","patch":`+raw+`}`), &q))
		_, err := client.callPayload(context.Background(), q, "/restart", false)
		require.Error(t, err)
		require.Zero(t, p.calls.Load())
	}
	patch, err := jobrestart.ParsePatch([]byte(`[{"op":"add","path":"/sequence/s/outputs/x/-","value":9007199254740993}]`))
	require.NoError(t, err)
	_, err = client.Restart(context.Background(), jobrestart.Request{Operation: "submit", JobID: "job", Selector: jobrestart.Selector{Scope: "s"}, Patch: patch})
	require.NoError(t, err)
	require.Equal(t, int32(1), p.calls.Load())
}

func TestRestartOperationArraysFailAgainstLegacyHelper(t *testing.T) {
	// This is the old helper's struct-shaped decoder, retained as a wire fixture.
	// Arrays must fail at decoding, before it can call its submission backend.
	var calls atomic.Int32
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Patch *struct {
				Job    map[string]any `json:"job"`
				Scopes []any          `json:"scopes"`
			} `json:"patch"`
		}
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			http.Error(w, "invalid restart request", 400)
			return
		}
		calls.Add(1)
		w.WriteHeader(200)
	}))
	defer old.Close()
	p, err := jobrestart.ParsePatch([]byte(`[{"op":"add","path":"/sequence/s/outputs/x","value":1}]`))
	require.NoError(t, err)
	_, err = (JobClient{Broker: jobcontext.ChildJobBroker{Endpoint: old.URL, Token: "fixture", SessionID: "fixture"}}).Restart(context.Background(), jobrestart.Request{Operation: "submit", JobID: "job", Patch: p})
	require.ErrorContains(t, err, "400")
	require.Zero(t, calls.Load())
}
