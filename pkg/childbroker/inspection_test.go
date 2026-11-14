package childbroker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"io"
	"net/http"
	"net/http/httptest"
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

type familyProbe struct {
	inspectionProbe
	failure  error
	response *jobinspect.Response
}

func (p *familyProbe) Inspect(_ context.Context, q jobinspect.Request) (*jobinspect.Response, error) {
	p.calls.Add(1)
	if p.failure != nil {
		return nil, p.failure
	}
	return p.response, nil
}
func TestFamilyHelperProtocolAndSafeErrors(t *testing.T) {
	probe := &familyProbe{response: &jobinspect.Response{Version: 1, Family: &jobinspect.FamilyPage{Version: 1, Nodes: []jobinspect.FamilyNode{}}}}
	server, err := Start(jobinspect.WithAPI(context.Background(), probe), Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "job", GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer server.Close()
	broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, err)
	for _, operation := range []string{"family-node", "family-children"} {
		q := jobinspect.Request{Operation: operation, JobID: "selected", Limit: 100, Family: &jobinspect.FamilyOptions{Version: 1}}
		for _, bad := range []string{"token", "session"} {
			b := broker
			if bad == "token" {
				b.Token = "bad"
			} else {
				b.SessionID = "bad"
			}
			_, err = (JobClient{Broker: b}).Inspect(context.Background(), q)
			require.Error(t, err)
		}
	}
	require.Zero(t, probe.calls.Load())
	q := jobinspect.Request{Operation: "family-children", JobID: "selected", Limit: 100, Family: &jobinspect.FamilyOptions{Version: 1}}
	client := JobClient{Broker: broker}
	_, err = client.Inspect(context.Background(), q)
	require.NoError(t, err)
	for _, failure := range []error{jobdb.ErrLeasePermissionDenied, jobdb.ErrExecutionLeaseLost, context.Canceled, context.DeadlineExceeded, &jobdb.CellError{Code: "permission_denied", Status: 403}, errors.New("opaque backend failure")} {
		probe.failure = failure
		_, err = client.Inspect(context.Background(), q)
		require.Error(t, err)
		require.Equal(t, jobinspect.FamilyErrorReason(failure), jobinspect.FamilyErrorReason(err))
		if jobinspect.FamilyErrorReason(failure) == "permission_denied" {
			require.NotContains(t, err.Error(), "upgrade")
		}
	}
	probe.failure = nil
	for _, r := range []*jobinspect.Response{{Version: 1}, {Version: 1, Family: &jobinspect.FamilyPage{Version: 2, Nodes: []jobinspect.FamilyNode{}}}, {Version: 1, Family: &jobinspect.FamilyPage{Version: 1}}} {
		probe.response = r
		_, err = client.Inspect(context.Background(), q)
		require.ErrorContains(t, err, "upgrade")
	}
}
func TestFamilyHelperRejectsScopeOverrides(t *testing.T) {
	rt := &familyBackendProbe{}
	api := &jobinspect.Service{Tenant: "tenant", Runtime: rt}
	server, err := Start(jobinspect.WithAPI(context.Background(), api), Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "job", GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer server.Close()
	b, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, err)
	for _, raw := range []string{
		`{"operation":"family-children","job_id":"job","limit":100,"family":{"version":1},"tenant_id":"other"}`,
		`{"operation":"family-children","job_id":"job","limit":100,"family":{"version":1,"tenant":"other"}}`,
		`{"operation":"family-children","job_id":"job","limit":100,"family":{"version":1},"parent":"other"}`,
		`{"operation":"family-node","job_id":"job","limit":100,"family":{"version":1},"task":"other"}`,
	} {
		req, err := http.NewRequest(http.MethodPost, b.Endpoint+"/inspect", strings.NewReader(raw))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+b.Token)
		req.Header.Set(sessionHeader, b.SessionID)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, 400, resp.StatusCode)
	}
	require.Zero(t, rt.calls)
}

type familyBackendProbe struct {
	jobdb.WorkflowRuntime
	calls int
}

func (p *familyBackendProbe) ListJobs(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	p.calls++
	return jobdb.ListJobsResponse{}, nil
}
func TestFamilyLegacyHelperRejection(t *testing.T) {
	calls := 0
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An old helper knows no family marker. Reject it before entering its service.
		var old struct {
			Operation string `json:"operation"`
			JobID     string `json:"job_id"`
			Limit     int    `json:"limit"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&old) != nil {
			http.Error(w, "invalid inspection request", 400)
			return
		}
		calls++
		http.Error(w, "unexpected service read", 500)
	}))
	defer legacy.Close()
	client := JobClient{Broker: jobcontext.ChildJobBroker{Endpoint: legacy.URL, Token: "token", SessionID: "session"}}
	_, err := client.Inspect(context.Background(), jobinspect.Request{Operation: "family-node", JobID: "selected", Limit: 100, Family: &jobinspect.FamilyOptions{Version: 1}})
	require.ErrorContains(t, err, "upgrade")
	require.Zero(t, calls)
}
