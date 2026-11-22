package childbroker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinputs"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type inputProbe struct {
	checks, calls atomic.Int32
	revoked       atomic.Bool
	payload       []byte
	source        io.ReadCloser
}

func (p *inputProbe) Check(ctx context.Context, _ string) error {
	p.checks.Add(1)
	if p.revoked.Load() {
		return jobdb.ErrLeasePermissionDenied
	}
	return context.Cause(ctx)
}
func (p *inputProbe) Inspect(ctx context.Context, q jobinputs.Request) (*jobinputs.Response, error) {
	p.calls.Add(1)
	if e := p.Check(ctx, q.JobID); e != nil {
		return nil, e
	}
	if q.JobID != "job" {
		return nil, jobdb.ErrJobNotFound
	}
	return &jobinputs.Response{Version: 1, Page: json.RawMessage(`{"job_id":"job","snapshot_token":"s","completeness":"complete","entries":[]}`)}, nil
}
func (p *inputProbe) Open(ctx context.Context, q jobinputs.Request) (*jobinputs.Artifact, error) {
	p.calls.Add(1)
	if e := p.Check(ctx, q.JobID); e != nil {
		return nil, e
	}
	if p.source != nil {
		return &jobinputs.Artifact{ReadCloser: p.source, SizeBytes: int64(len(p.payload))}, nil
	}
	return &jobinputs.Artifact{ReadCloser: jobinputs.VerifiedStream(ctx, io.NopCloser(bytes.NewReader(p.payload)), int64(len(p.payload)), nil), SizeBytes: int64(len(p.payload))}, nil
}

// Deliberately ignore cancellation so the test exercises the helper's dispatch
// boundary rather than relying on the API or stream to reject an expired call.
type inputHistoryDispatchProbe struct{ calls, checks atomic.Int32 }

func (p *inputHistoryDispatchProbe) Inspect(_ context.Context, q jobinputs.Request) (*jobinputs.Response, error) {
	p.calls.Add(1)
	if q.Operation == "show" {
		return &jobinputs.Response{Version: 1, Entry: json.RawMessage(`{"entry_id":"entry","snapshot_token":"snapshot","completeness":"complete"}`)}, nil
	}
	return &jobinputs.Response{Version: 1, Page: json.RawMessage(`{"job_id":"job","snapshot_token":"snapshot","completeness":"complete","entries":[]}`)}, nil
}
func (p *inputHistoryDispatchProbe) Open(context.Context, jobinputs.Request) (*jobinputs.Artifact, error) {
	p.calls.Add(1)
	return &jobinputs.Artifact{ReadCloser: io.NopCloser(strings.NewReader("retained bytes")), SizeBytes: int64(len("retained bytes"))}, nil
}
func (p *inputHistoryDispatchProbe) Check(context.Context, string) error {
	p.checks.Add(1)
	return nil
}

func TestInputHistoryHelperExpiredLifetimeBeforeDispatch(t *testing.T) {
	for _, lifetime := range []string{"live", "request-canceled", "request-deadline", "execution-canceled", "execution-deadline"} {
		for _, operation := range []string{"list", "show", "artifact"} {
			t.Run(lifetime+"/"+operation, func(t *testing.T) {
				probe := &inputHistoryDispatchProbe{}
				execution, endExecution := context.WithCancel(context.Background())
				defer endExecution()
				request, endRequest := context.WithCancel(context.Background())
				defer endRequest()
				wantCode, wantCause := "canceled", context.Canceled
				switch lifetime {
				case "request-canceled":
					endRequest()
				case "execution-canceled":
					endExecution()
				case "request-deadline":
					var stop context.CancelFunc
					request, stop = context.WithDeadline(context.Background(), time.Time{})
					defer stop()
					wantCode, wantCause = "timeout", context.DeadlineExceeded
				case "execution-deadline":
					var stop context.CancelFunc
					execution, stop = context.WithDeadline(context.Background(), time.Time{})
					defer stop()
					wantCode, wantCause = "timeout", context.DeadlineExceeded
				}
				server := &Server{executionContext: execution, inputs: probe, current: jobcontext.Current{JobID: "job"}, token: "token", sessionID: "session"}
				q := jobinputs.Request{Version: 1, Operation: operation, JobID: "self"}
				if operation != "list" {
					q.Snapshot, q.EntryID = "snapshot", "entry"
				}
				path := "/inspect/inputs"
				if operation == "artifact" {
					q.ArtifactID, path = "member", path+"/artifact"
				}
				raw, err := json.Marshal(q)
				require.NoError(t, err)
				r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)).WithContext(request)
				r.Header.Set("Authorization", "Bearer token")
				r.Header.Set(sessionHeader, "session")
				w := httptest.NewRecorder()
				server.handleInputHistory(w, r)
				require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
				if lifetime == "live" {
					require.Equal(t, int32(1), probe.calls.Load())
					require.Equal(t, http.StatusOK, w.Code)
					if operation == "artifact" {
						require.Equal(t, "retained bytes", w.Body.String())
						require.Equal(t, "1", w.Header().Get("X-C2J-Download-Version"))
						require.Equal(t, int32(1), probe.checks.Load())
					} else {
						_, err := jobinputs.DecodeResponse(w.Body, operation)
						require.NoError(t, err)
					}
					return
				}
				require.Zero(t, probe.calls.Load(), "expired lifetime must not enter Inspect or Open")
				require.Zero(t, probe.checks.Load(), "expired request must not reach stream completion")
				require.Equal(t, http.StatusBadRequest, w.Code)
				require.Empty(t, w.Header().Get("X-C2J-Download-Version"))
				diagnostic, err := jobinputs.DecodeError(w.Body.Bytes())
				require.NoError(t, err)
				require.Equal(t, wantCode, diagnostic.Code)
				require.ErrorIs(t, diagnostic, wantCause)
			})
		}
	}
}

func TestInputHistoryHelperAuthenticationAndClaims(t *testing.T) {
	probe := &inputProbe{payload: []byte{0, 255, 10}}
	var factories atomic.Int32
	ctx := jobinputs.WithFactory(context.Background(), func() (jobinputs.API, error) { factories.Add(1); return probe, nil })
	server, e := Start(ctx, Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "job", GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, e)
	defer server.Close()
	broker, _, e := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, e)
	client := JobClient{Broker: broker}
	q := jobinputs.Request{Version: 1, Operation: "list", JobID: "self"}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := client.Inputs(context.Background(), q)
			require.NoError(t, e)
			require.NotNil(t, r)
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), factories.Load())
	calls := probe.calls.Load()
	for _, field := range []string{`"tenant_id":"forged"`, `"credential":"ambient"`, `"role":"agent"`, `"producer_key":"raw"`, `"unknown":true`} {
		raw := `{"version":1,"operation":"list","job_id":"self",` + field + `}`
		request, e := http.NewRequest(http.MethodPost, broker.Endpoint+"/inspect/inputs", strings.NewReader(raw))
		require.NoError(t, e)
		request.Header.Set("Authorization", "Bearer "+broker.Token)
		request.Header.Set(sessionHeader, broker.SessionID)
		response, e := http.DefaultClient.Do(request)
		require.NoError(t, e)
		b, e := io.ReadAll(response.Body)
		require.NoError(t, e)
		response.Body.Close()
		d, e := jobinputs.DecodeError(b)
		require.NoError(t, e)
		require.Equal(t, "invalid_request", d.Code)
	}
	require.Equal(t, calls, probe.calls.Load())
	for _, broken := range []jobcontext.ChildJobBroker{func() jobcontext.ChildJobBroker { b := broker; b.Token = "forged"; return b }(), func() jobcontext.ChildJobBroker { b := broker; b.SessionID = "forged"; return b }()} {
		_, e := (JobClient{Broker: broken}).Inputs(context.Background(), q)
		require.ErrorIs(t, e, jobdb.ErrLeasePermissionDenied)
	}
	require.Equal(t, calls, probe.calls.Load())
	a, e := client.InputArtifact(context.Background(), jobinputs.Request{Version: 1, Operation: "artifact", JobID: "self", EntryID: "e", Snapshot: "s", ArtifactID: "m"})
	require.NoError(t, e)
	b, e := io.ReadAll(a)
	require.NoError(t, e)
	require.Equal(t, probe.payload, b)
	require.NoError(t, a.Close())
}

type completionSource struct {
	probe  *inputProbe
	read   bool
	closes atomic.Int32
}

func (s *completionSource) Read(p []byte) (int, error) {
	if !s.read {
		s.read = true
		return copy(p, "abc"), nil
	}
	return 0, io.EOF
}
func (s *completionSource) Close() error { s.closes.Add(1); s.probe.revoked.Store(true); return nil }
func TestInputHistoryHelperFinalAuthority(t *testing.T) {
	probe := &inputProbe{payload: []byte("abc")}
	source := &completionSource{probe: probe}
	probe.source = source
	server, e := Start(jobinputs.WithFactory(context.Background(), func() (jobinputs.API, error) { return probe, nil }), Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "job", GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, e)
	defer server.Close()
	broker, _, e := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, e)
	a, e := (JobClient{Broker: broker}).InputArtifact(context.Background(), jobinputs.Request{Version: 1, Operation: "artifact", JobID: "job", Snapshot: "s", EntryID: "e", ArtifactID: "m"})
	require.NoError(t, e)
	b, e := io.ReadAll(a)
	require.Equal(t, "abc", string(b))
	require.ErrorIs(t, e, jobdb.ErrLeasePermissionDenied)
	require.Error(t, a.Close())
}
func TestInputHistoryHelperTransport(t *testing.T) {
	for _, mode := range []string{"control-no-marker", "control-content-length", "missing-size", "invalid-size", "negative-size", "duplicate-size", "short", "excess", "malformed", "failed", "version"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("X-C2J-Download-Version", "1")
				w.Header().Set("X-C2J-Download-Size", "3")
				if mode == "version" {
					w.Header().Set("X-C2J-Download-Version", "9")
				}
				switch mode {
				case "missing-size":
					w.Header().Del("X-C2J-Download-Size")
				case "invalid-size":
					w.Header().Set("X-C2J-Download-Size", "three")
				case "negative-size":
					w.Header().Set("X-C2J-Download-Size", "-1")
				case "duplicate-size":
					w.Header().Add("X-C2J-Download-Size", "3")
				case "short":
					w.Header().Set("X-C2J-Download-Size", "4")
				case "excess":
					w.Header().Set("X-C2J-Download-Size", "2")
				}
				if mode == "control-content-length" {
					w.Header().Set("Content-Length", "3")
				} else {
					w.Header().Set("Trailer", "X-C2J-Download-Error")
				}
				io.WriteString(w, "abc")
				if mode == "failed" {
					raw, _ := json.Marshal(jobinputs.Diagnostic("lease_lost", "expired", jobdb.ErrExecutionLeaseLost))
					w.Header().Set("X-C2J-Download-Error", base64.RawURLEncoding.EncodeToString(raw))
				}
				if mode == "malformed" {
					w.Header().Set("X-C2J-Download-Error", "?")
				}
			}))
			defer server.Close()
			a, e := (JobClient{Broker: jobcontext.ChildJobBroker{Endpoint: server.URL, Token: "t", SessionID: "s"}}).InputArtifact(context.Background(), jobinputs.Request{Version: 1, Operation: "artifact", JobID: "j", EntryID: "e", ArtifactID: "m", Snapshot: "s"})
			if mode == "version" || strings.HasSuffix(mode, "-size") {
				require.Error(t, e)
				return
			}
			require.NoError(t, e)
			_, e = io.ReadAll(a)
			if strings.HasPrefix(mode, "control-") {
				require.NoError(t, e)
				require.NoError(t, a.Close())
			} else {
				require.Error(t, e)
				require.Error(t, a.Close())
			}
		})
	}
}
