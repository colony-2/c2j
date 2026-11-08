package childbroker

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/stretchr/testify/require"
)

type inspectionProbe struct {
	calls atomic.Int32
}

func (p *inspectionProbe) Inspect(_ context.Context, q jobinspect.Request) (*jobinspect.Response, error) {
	p.calls.Add(1)
	return &jobinspect.Response{Version: jobinspect.Version, Job: &jobinspect.Job{ID: q.JobID}}, nil
}
func (p *inspectionProbe) Download(context.Context, jobinspect.Request) (io.ReadCloser, error) {
	p.calls.Add(1)
	return io.NopCloser(strings.NewReader("bytes")), nil
}

// INS-020, INS-029: both endpoints require the same task authentication.
func TestJobInspectionAuthentication(t *testing.T) {
	probe := &inspectionProbe{}
	server, err := Start(jobinspect.WithAPI(context.Background(), probe), Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "job", GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer server.Close()
	broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, err)
	for _, bad := range []string{"token", "session"} {
		b := broker
		if bad == "token" {
			b.Token = "invalid"
		} else {
			b.SessionID = "invalid"
		}
		client := JobClient{Broker: b}
		_, err = client.Inspect(context.Background(), jobinspect.Request{Operation: "show", JobID: "other"})
		require.Error(t, err)
		_, err = client.Download(context.Background(), jobinspect.Request{JobID: "other", Name: "x"})
		require.Error(t, err)
	}
	require.Zero(t, probe.calls.Load())
	client := JobClient{Broker: broker}
	r, err := client.Inspect(context.Background(), jobinspect.Request{Operation: "show", JobID: "other"})
	require.NoError(t, err)
	require.Equal(t, "other", r.Job.ID)
	bytes, err := client.Download(context.Background(), jobinspect.Request{JobID: "other", Name: "x"})
	require.NoError(t, err)
	data, err := io.ReadAll(bytes)
	require.NoError(t, err)
	require.NoError(t, bytes.Close())
	require.Equal(t, "bytes", string(data))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Inspect(canceled, jobinspect.Request{Operation: "show", JobID: "other"})
	require.Error(t, err)
	_, err = client.Download(canceled, jobinspect.Request{JobID: "other", Name: "x"})
	require.Error(t, err)
	require.Equal(t, int32(2), probe.calls.Load())
}

func TestJobEventsRejectScopeOverrides(t *testing.T) {
	probe := &inspectionProbe{}
	server, err := Start(jobinspect.WithAPI(context.Background(), probe), Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "job", GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer server.Close()
	broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, err)
	for _, raw := range []string{
		`{"operation":"events","job_id":"selected","tenant_id":"other"}`,
		`{"operation":"events","job_id":"selected","events":{"job_ids":["other"]}}`,
		`{"operation":"events","job_id":"selected","events":{"tenant_id":"other"}}`,
		`{"operation":"events","job_id":"selected","events":{"event_types":null}}`,
		`{"operation":"events","job_id":"selected","events":{"event_types":[]}}`,
	} {
		req, err := http.NewRequest(http.MethodPost, strings.TrimRight(broker.Endpoint, "/")+"/inspect", strings.NewReader(raw))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+broker.Token)
		req.Header.Set(sessionHeader, broker.SessionID)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	}
	require.Zero(t, probe.calls.Load())
}
