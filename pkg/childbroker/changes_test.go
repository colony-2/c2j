package childbroker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

type changesErrorProbe struct {
	*jobinspect.Service
	failure error
}

type changesReadSignal struct {
	io.Reader
	once sync.Once
	read chan struct{}
}

func (r *changesReadSignal) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.read) })
	return r.Reader.Read(p)
}

func (p *changesErrorProbe) History(ctx context.Context, q jobinspect.ChangesRequest) (*jobinspect.ChangesHistory, error) {
	if p.failure != nil {
		return nil, p.failure
	}
	return p.Service.History(ctx, q)
}
func (p *changesErrorProbe) OpenDiff(ctx context.Context, q jobinspect.DiffRequest) (*jobinspect.ChangesDiff, io.ReadCloser, error) {
	if p.failure != nil {
		return nil, nil, p.failure
	}
	return p.Service.OpenDiff(ctx, q)
}

func TestChangesHelperJ37J41L09(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, cancel := context.WithCancel(celltest.Context(context.Background()))
			defer cancel()
			rt, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "helper.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
			require.NoError(t, e)
			defer rt.Close(ctx)
			var runtime jobdb.ConsumerRuntime = rt
			if backend == "http" {
				server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(rt)))
				defer server.Close()
				client, e := remote.New(server.URL, server.Client())
				require.NoError(t, e)
				runtime = client
			}
			celltest.Create(t, runtime, "tenant", "changes", "example.test/changes")
			f, e := changestest.Build(ctx, runtime, changestest.Options{Directory: t.TempDir()})
			require.NoError(t, e)
			oracle, e := f.Oracle(ctx, f.Base, f.Head)
			require.NoError(t, e)
			require.NoError(t, f.DeleteCheckout())
			before, e := changestest.Inventory(ctx, runtime, f.Job)
			require.NoError(t, e)
			probe := &changesErrorProbe{Service: &jobinspect.Service{Runtime: runtime, Tenant: "tenant"}}
			server, e := Start(jobinspect.WithAPI(ctx, probe), Options{Current: jobcontext.Current{TenantID: "tenant", JobID: f.Job.JobId, GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
			require.NoError(t, e)
			defer server.Close()
			broker, _, e := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
			require.NoError(t, e)
			client := JobClient{Broker: broker}
			q := jobinspect.ChangesRequest{JobID: f.Job.JobId}
			h, e := client.History(ctx, q)
			require.NoError(t, e)
			require.Equal(t, f.Head, h.Head)
			meta, body, e := client.OpenDiff(ctx, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId, Selection: h.Selection}})
			require.NoError(t, e)
			data, e := io.ReadAll(body)
			require.NoError(t, e)
			require.NoError(t, body.Close())
			require.Equal(t, oracle, data)
			require.Equal(t, h.Selection, meta.Selection)
			for _, code := range []string{"stale_selection", "job_denied", "limit", "missing_closure", "backend", "repository_denied", "repository_authentication"} {
				probe.failure = &jobinspect.ChangesError{Version: 1, Code: code, Detail: "safe detail", Missing: []string{f.Base}, Limit: "objects"}
				_, e = client.History(ctx, q)
				var diagnostic *jobinspect.ChangesError
				require.ErrorAs(t, e, &diagnostic)
				require.Equal(t, code, diagnostic.Code)
				require.Equal(t, []string{f.Base}, diagnostic.Missing)
			}
			for _, failure := range []error{context.Canceled, context.DeadlineExceeded, jobdb.ErrExecutionLeaseLost} {
				probe.failure = failure
				_, e = client.History(ctx, q)
				require.ErrorIs(t, e, failure)
			}
			probe.failure = nil
			testChangesMetadataInterruptions(t, ctx, h, q)
			for _, raw := range []string{`{"job_id":"x","tenant":"other"}`, `{"job_id":"x","task":"other"}`, `{"job_id":"x","attempt":1}`, `{"job_id":"x","workspace":"other"}`, `{"job_id":"x","review":"other"}`, `{"job_id":"x","context":"other"}`, `{"job_id":"x","repository":"other"}`, `{"job_id":"x","artifact":{}}`, `{"job_id":"x","reference":{}}`} {
				req, e := http.NewRequestWithContext(ctx, "POST", broker.Endpoint+"/inspect/changes", strings.NewReader(raw))
				require.NoError(t, e)
				req.Header.Set("Authorization", "Bearer "+broker.Token)
				req.Header.Set(sessionHeader, broker.SessionID)
				resp, e := http.DefaultClient.Do(req)
				require.NoError(t, e)
				require.Equal(t, 400, resp.StatusCode)
				resp.Body.Close()
			}
			// Faults occur after real writer/backend setup. Missing terminal status,
			// short bytes and unexpected suffixes must never become a successful EOF.
			header, e := json.Marshal(meta)
			require.NoError(t, e)
			for _, wire := range []string{string(header) + "\n" + string(oracle[:len(oracle)/2]), string(header) + "\n" + string(oracle), string(header) + "\n" + string(oracle) + changesTerminal + "extra"} {
				fault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, wire) }))
				faultClient := JobClient{Broker: jobcontext.ChildJobBroker{Endpoint: fault.URL}}
				_, r, e := faultClient.OpenDiff(ctx, jobinspect.DiffRequest{ChangesRequest: q})
				require.NoError(t, e)
				_, e = io.ReadAll(r)
				require.Error(t, e)
				require.NoError(t, r.Close())
				fault.Close()
			}
			// Cancel after real metadata has arrived, at every blocking phase of
			// body consumption. A transport diagnostic must not erase ctx.Err().
			for _, phase := range []struct{ name, prefix string }{
				{"payload", ""},
				{"partial-payload", string(oracle[:len(oracle)/2])},
				{"terminal", string(oracle)},
				{"partial-terminal", string(oracle) + changesTerminal[:8]},
				{"suffix", string(oracle) + changesTerminal},
			} {
				for _, deadline := range []bool{false, true} {
					name := "cancel-" + phase.name
					if deadline {
						name = "deadline-" + phase.name
					}
					t.Run(name, func(t *testing.T) {
						fault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							_, _ = io.WriteString(w, string(header)+"\n"+phase.prefix)
							w.(http.Flusher).Flush()
							<-r.Context().Done()
						}))
						defer fault.Close()
						streamCtx, stop := context.WithCancel(ctx)
						want, code := context.Canceled, "canceled"
						if deadline {
							stop()
							streamCtx, stop = context.WithTimeout(ctx, time.Second)
							want, code = context.DeadlineExceeded, "deadline"
						}
						defer stop()
						faultClient := JobClient{Broker: jobcontext.ChildJobBroker{Endpoint: fault.URL}}
						_, body, err := faultClient.OpenDiff(streamCtx, jobinspect.DiffRequest{ChangesRequest: q})
						require.NoError(t, err)
						defer body.Close()
						// Preserve bytes prefetched with the metadata, then signal
						// when consumption actually reaches the held-open HTTP body.
						stream := body.(*changesBody)
						pending, err := stream.reader.Peek(stream.reader.Buffered())
						require.NoError(t, err)
						blocked := make(chan struct{})
						stream.reader = bufio.NewReader(io.MultiReader(bytes.NewReader(append([]byte(nil), pending...)), &changesReadSignal{Reader: stream.body, read: blocked}))
						finished := make(chan error, 1)
						go func() { _, err := io.ReadAll(body); finished <- err }()
						select {
						case <-blocked:
						case <-time.After(5 * time.Second):
							t.Fatal("stream did not reach the held-open HTTP body")
						}
						if !deadline {
							stop()
						}
						select {
						case err = <-finished:
						case <-time.After(5 * time.Second):
							t.Fatal("canceled stream did not release its read")
						}
						require.ErrorIs(t, err, want)
						require.Equal(t, code, jobinspect.ChangesDiagnostic(err).Code)
						_, next := body.Read(make([]byte, 1))
						require.ErrorIs(t, next, want, "stream failure must remain sticky")
						require.NoError(t, body.Close())
					})
				}
			}
			// Version negotiation errors are distinguishable from interrupted bytes.
			badMeta := *meta
			badMeta.Version = 999
			badHeader, e := json.Marshal(badMeta)
			require.NoError(t, e)
			for _, reply := range []struct {
				status int
				body   string
			}{
				{200, string(badHeader) + "\n"},
				{400, `{"version":999,"code":"limit"}`},
				{400, `{"version":1,"code":"future_code"}`},
				{403, `{"version":999,"code":"limit"}`},
				{403, `{"version":1,"code":"future_code"}`},
			} {
				fault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(reply.status)
					_, _ = io.WriteString(w, reply.body)
				}))
				faultClient := JobClient{Broker: jobcontext.ChildJobBroker{Endpoint: fault.URL}}
				_, _, e = faultClient.OpenDiff(ctx, jobinspect.DiffRequest{ChangesRequest: q})
				var diagnostic *jobinspect.ChangesError
				require.ErrorAs(t, e, &diagnostic)
				require.Equal(t, "protocol", diagnostic.Code)
				fault.Close()
			}
			after, e := changestest.Inventory(ctx, runtime, f.Job)
			require.NoError(t, e)
			require.Equal(t, string(before), string(after))
		})
	}
}
