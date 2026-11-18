package childbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/stretchr/testify/require"
)

type metricProbe struct {
	inspectionProbe
	result *jobinspect.Response
	err    error
}

func (p *metricProbe) Inspect(_ context.Context, _ jobinspect.Request) (*jobinspect.Response, error) {
	p.calls.Add(1)
	return p.result, p.err
}

func TestMetricsHelperAuthenticationExactnessAndIncompleteHistory(t *testing.T) {
	const exact = int64(9007199254740993)
	snapshot := metrics.Empty(45)
	d := metrics.Descriptor{Name: "tokens", Kind: "counter", NumberType: "int64", Unit: "{token}"}
	snapshot.Series["tokens"] = []metrics.Point{{Descriptor: d, Value: exact}}
	p := &metricProbe{result: &jobinspect.Response{Version: jobinspect.Version, Metrics: &metrics.Result{
		Snapshot: snapshot,
		Reports:  []metrics.Record{{Channel: "producer", ChannelReport: metrics.Channel{Status: "valid", Report: &metrics.Report{Version: 1, Mutations: []metrics.Mutation{{Descriptor: d, Op: "add", Value: exact}}}}}},
	}}}
	execution, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := Start(jobinspect.WithAPI(execution, p), Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "current", GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer server.Close()
	b, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, err)
	q := jobinspect.Request{Operation: "metrics", JobID: "selected", Metrics: &metrics.Query{Deltas: true}}
	for _, field := range []string{"token", "session"} {
		bad := b
		if field == "token" {
			bad.Token = "bad"
		} else {
			bad.SessionID = "bad"
		}
		_, err := (JobClient{Broker: bad}).Inspect(context.Background(), q)
		require.ErrorContains(t, err, "401")
	}
	require.Zero(t, p.calls.Load())
	// Tenant selection belongs to the authenticated worker, never request data.
	_, err = (JobClient{Broker: b}).callPayload(context.Background(), map[string]any{"operation": "metrics", "job_id": "selected", "tenant_id": "other"}, "/inspect", false)
	require.ErrorContains(t, err, "400")
	require.Zero(t, p.calls.Load())
	client := JobClient{Broker: b}
	result, err := client.Inspect(context.Background(), q)
	require.NoError(t, err)
	require.Equal(t, exact, result.Metrics.Snapshot.Series["tokens"][0].Value)
	require.Equal(t, exact, result.Metrics.Reports[0].ChannelReport.Report.Mutations[0].Value)
	p.result.Metrics.Snapshot.Coverage.History = "unavailable"
	p.err = errors.New("history read failed")
	result, err = client.Inspect(context.Background(), q)
	require.ErrorContains(t, err, "unavailable")
	require.NotNil(t, result, "HTTP 422 retains the diagnostic snapshot")
	require.Equal(t, "unavailable", result.Metrics.Snapshot.Coverage.History)
	require.Equal(t, int32(2), p.calls.Load())
	cancel()
	_, err = client.Inspect(context.Background(), q)
	require.Error(t, err)
	require.Equal(t, int32(2), p.calls.Load(), "expired task scope cannot enter inspection")
}

func TestMetricsHelperRequiresWorkerCapability(t *testing.T) {
	for _, response := range []string{"old-decoder", "old-operation", "missing-metrics", "future-reducer"} {
		t.Run(response, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch response {
				case "old-decoder":
					http.Error(w, "invalid inspection request", 400)
				case "old-operation":
					http.Error(w, "unknown inspection operation", 400)
				case "missing-metrics":
					_ = json.NewEncoder(w).Encode(jobinspect.Response{Version: 1})
				case "future-reducer":
					s := metrics.Empty(1)
					s.ReducerVersion++
					_ = json.NewEncoder(w).Encode(jobinspect.Response{Version: 1, Metrics: &metrics.Result{Snapshot: s}})
				}
			}))
			defer server.Close()
			client := JobClient{Broker: jobcontext.ChildJobBroker{Endpoint: server.URL, Token: "token", SessionID: "session"}}
			_, err := client.Inspect(context.Background(), jobinspect.Request{Operation: "metrics", JobID: "selected", Metrics: &metrics.Query{}})
			require.ErrorContains(t, err, "upgrade the worker and CLI together")
			require.True(t, strings.Contains(err.Error(), "metrics inspection unavailable"))
			require.Equal(t, int32(1), calls.Load(), "no fallback or retry against ambient credentials")
		})
	}
}
