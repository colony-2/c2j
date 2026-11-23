package childbroker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/workspacedifftest"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

// Named artifact reads predate saved diffs and keep their original diagnostics.
// Exact-reference saved-diff reads still need typed absence and lease failures.
func TestWorkspaceDiffHelperDownloadErrorCompatibility(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
		code string
	}{
		{"external", errors.New("external artifact downloads are unavailable"), "storage"},
		{"backend", errors.New("backend denied artifact"), "storage"},
		{"missing", fs.ErrNotExist, "missing"},
		{"denied", jobdb.ErrLeasePermissionDenied, "denied"},
		{"expired", jobdb.ErrExecutionLeaseLost, "lease_lost"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			api := &workspaceDownloadFailureAPI{failure: failure.err}
			server, err := Start(jobinspect.WithAPI(context.Background(), api), Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "job", GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
			require.NoError(t, err)
			defer server.Close()
			broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
			require.NoError(t, err)
			client := JobClient{Broker: broker}
			for _, operation := range []string{"", "artifact"} {
				body, err := client.Download(context.Background(), jobinspect.Request{Operation: operation, JobID: "job", Name: "check.bin"})
				require.Nil(t, body)
				require.ErrorContains(t, err, failure.err.Error())
				var typed *jobinspect.WorkspaceDiffError
				require.False(t, errors.As(err, &typed), "named downloads retain their original error contract")
			}
			key := jobdb.ArtifactKey{JobId: "job", TaskOrdinal: 1, Name: "diff_from_base.diff", SizeBytes: 1}
			body, err := client.Download(context.Background(), jobinspect.Request{Operation: "artifact", JobID: "job", Reference: &key})
			require.Nil(t, body)
			var typed *jobinspect.WorkspaceDiffError
			require.ErrorAs(t, err, &typed)
			require.Equal(t, failure.code, typed.Code)
			if failure.code == "denied" || failure.code == "lease_lost" {
				require.ErrorIs(t, err, failure.err)
			}
		})
	}
}

type workspaceDownloadFailureAPI struct {
	jobinspect.API
	failure error
}

func (a *workspaceDownloadFailureAPI) Download(context.Context, jobinspect.Request) (io.ReadCloser, error) {
	return nil, a.failure
}

func TestWorkspaceDiffHelperI06I07I08M03E04E05(t *testing.T) {
	ctx := celltest.Context(context.Background())
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "helper.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	defer db.Close(ctx)
	celltest.Create(t, db, "saved-tests", "saved", "example.test/saved")
	fixture, err := workspacedifftest.Build(ctx, db, t.TempDir())
	require.NoError(t, err)
	service := &jobinspect.Service{Runtime: db, Tenant: fixture.Job.TenantId}
	server, err := Start(jobinspect.WithAPI(ctx, service), Options{Current: jobcontext.Current{TenantID: fixture.Job.TenantId, JobID: fixture.Job.JobId, GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer server.Close()
	broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, err)
	q := jobinspect.WorkspaceDiffRequest{JobID: fixture.Job.JobId}
	client := JobClient{Broker: broker}
	meta, body, err := client.OpenWorkspaceDiff(ctx, q)
	require.NoError(t, err)
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, fixture.Steps[2].Base, data)
	for _, bad := range []string{"token", "session"} {
		b := broker
		if bad == "token" {
			b.Token = "wrong"
		} else {
			b.SessionID = "wrong"
		}
		_, _, err := (JobClient{Broker: b}).OpenWorkspaceDiff(ctx, q)
		require.Error(t, err)
	}
	for _, raw := range []string{`{"operation":"workspace_diff","job_id":"job","task":""}`, `{"operation":"workspace_diff","job_id":"job","workspace_diff":null}`, `{"operation":"workspace_diff","job_id":"job","workspace_diff":{"attempt":0}}`, `{"operation":"workspace_diff","job_id":"job","workspace_diff":{"task":"x","task":"y"}}`} {
		req, err := http.NewRequest(http.MethodPost, broker.Endpoint+"/inspect", strings.NewReader(raw))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+broker.Token)
		req.Header.Set(sessionHeader, broker.SessionID)
		response, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, 400, response.StatusCode)
		require.NoError(t, response.Body.Close())
	}
	for _, variant := range []string{"version", "request", "source", "size", "availability", "old-helper", "missing-completion", "malformed-completion", "unknown-completion"} {
		t.Run(variant, func(t *testing.T) {
			raw, _ := json.Marshal(meta)
			var m jobinspect.WorkspaceDiff
			require.NoError(t, json.Unmarshal(raw, &m))
			var downloads atomic.Int32
			switch variant {
			case "version":
				m.Version = 2
			case "request":
				m.Requested.JobID = "other"
			case "source":
				m.Source.Producer.JobID = "other"
			case "size":
				n := *m.Bytes + 1
				m.Bytes = &n
			case "availability":
				m.Status = "empty"
			case "unknown-completion":
				m.Bytes = nil
				m.Source.Key.SizeBytes = -1
			}
			helper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if variant == "old-helper" {
					http.Error(w, "unknown inspection operation", 400)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/download") {
					downloads.Add(1)
					w.Header().Set("X-C2J-Download-Version", "1")
					if variant == "malformed-completion" {
						w.Header().Set("Trailer", "X-C2J-Download-Error")
						w.Write(data)
						w.Header().Set("X-C2J-Download-Error", "invalid-base64!")
						return
					}
					w.Write(data)
					return
				}
				require.NoError(t, json.NewEncoder(w).Encode(jobinspect.Response{Version: 1, WorkspaceDiff: &m}))
			}))
			defer helper.Close()
			fake := JobClient{Broker: jobcontext.ChildJobBroker{Endpoint: helper.URL, Token: "token", SessionID: "session"}}
			_, body, err := fake.OpenWorkspaceDiff(ctx, q)
			if strings.Contains(variant, "completion") {
				require.NoError(t, err)
				_, err = io.ReadAll(body)
				require.Error(t, err)
				require.Error(t, body.Close())
				require.Equal(t, int32(1), downloads.Load())
			} else {
				require.Error(t, err)
				require.Zero(t, downloads.Load())
			}
		})
	}
	// A failure after the expected length must survive the existing exact-ref
	// route and the helper's completion trailer.
	fault := &workspaceCloseAPI{API: service}
	failed, err := Start(jobinspect.WithAPI(ctx, fault), Options{Current: jobcontext.Current{TenantID: fixture.Job.TenantId, JobID: fixture.Job.JobId, GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer failed.Close()
	b, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return failed.Env()[k] })
	require.NoError(t, err)
	_, body, err = (JobClient{Broker: b}).OpenWorkspaceDiff(ctx, q)
	require.NoError(t, err)
	actual, err := io.ReadAll(body)
	require.Equal(t, data, actual)
	var typed *jobinspect.WorkspaceDiffError
	require.ErrorAs(t, err, &typed)
	require.Equal(t, "close", typed.Code)
	require.Error(t, body.Close())
	require.Equal(t, int32(1), fault.closed.Load())
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = client.OpenWorkspaceDiff(canceled, q)
	require.ErrorIs(t, err, context.Canceled)
}

type workspaceCloseAPI struct {
	jobinspect.API
	closed atomic.Int32
}

func (a *workspaceCloseAPI) Download(ctx context.Context, q jobinspect.Request) (io.ReadCloser, error) {
	r, err := a.API.Download(ctx, q)
	if err != nil {
		return nil, err
	}
	return &workspaceCloseBody{ReadCloser: r, owner: a}, nil
}

type workspaceCloseBody struct {
	io.ReadCloser
	owner *workspaceCloseAPI
}

func (b *workspaceCloseBody) Close() error {
	b.owner.closed.Add(1)
	return errors.Join(b.ReadCloser.Close(), errors.New("source close failed"))
}

var _ jobinspect.WorkspaceDiffAPI = JobClient{}

type workspaceBlockedAPI struct {
	jobinspect.API
	entered chan struct{}
	closed  chan struct{}
}

func (a *workspaceBlockedAPI) Download(ctx context.Context, q jobinspect.Request) (io.ReadCloser, error) {
	body, err := a.API.Download(ctx, q)
	if err != nil {
		return nil, err
	}
	close(a.entered)
	return &workspaceBlockedBody{ReadCloser: body, closed: a.closed}, nil
}

type workspaceBlockedBody struct {
	io.ReadCloser
	closed chan struct{}
	once   sync.Once
}

func (b *workspaceBlockedBody) Read([]byte) (int, error) { <-b.closed; return 0, io.ErrClosedPipe }
func (b *workspaceBlockedBody) Close() error {
	var err error
	b.once.Do(func() { err = b.ReadCloser.Close(); close(b.closed) })
	return err
}
func TestWorkspaceDiffHelperI07E06CancellationDuringDownload(t *testing.T) {
	ctx := celltest.Context(context.Background())
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "cancellation.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	defer db.Close(ctx)
	celltest.Create(t, db, "saved-tests", "saved", "example.test/saved")
	fixture, err := workspacedifftest.Build(ctx, db, t.TempDir())
	require.NoError(t, err)
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, jobdb.ErrExecutionLeaseLost} {
		t.Run(cause.Error(), func(t *testing.T) {
			lease, expire := context.WithCancelCause(ctx)
			defer expire(nil)
			api := &workspaceBlockedAPI{API: &jobinspect.Service{Runtime: db, Tenant: fixture.Job.TenantId}, entered: make(chan struct{}), closed: make(chan struct{})}
			server, err := Start(jobinspect.WithAPI(lease, api), Options{Current: jobcontext.Current{TenantID: fixture.Job.TenantId, JobID: fixture.Job.JobId, GoalID: "saved"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
			require.NoError(t, err)
			defer server.Close()
			broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
			require.NoError(t, err)
			result := make(chan error, 1)
			go func() {
				_, body, err := (JobClient{Broker: broker}).OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: fixture.Job.JobId})
				if err == nil {
					_, err = io.ReadAll(body)
					err = errors.Join(err, body.Close())
				}
				result <- err
			}()
			select {
			case <-api.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("download was not opened")
			}
			expire(cause)
			select {
			case <-api.closed:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream not closed on lease cancellation")
			}
			select {
			case err := <-result:
				require.ErrorIs(t, err, cause)
			case <-time.After(5 * time.Second):
				t.Fatal("helper did not finish cancelled stream")
			}
		})
	}
}

type workspaceExpiryRuntime struct {
	jobinspect.Runtime
	expired atomic.Bool
}

func (r *workspaceExpiryRuntime) OpenArtifact(ctx context.Context, q jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	if r.expired.Load() && strings.HasPrefix(q.Name, "diff_from_") {
		return nil, fs.ErrNotExist
	}
	return r.Runtime.OpenArtifact(ctx, q)
}

type workspaceExpiryAPI struct {
	*jobinspect.Service
	runtime *workspaceExpiryRuntime
}

func (a *workspaceExpiryAPI) Inspect(ctx context.Context, q jobinspect.Request) (*jobinspect.Response, error) {
	m, err := a.Service.Inspect(ctx, q)
	if err == nil && q.Operation == "workspace_diff" {
		a.runtime.expired.Store(true)
	}
	return m, err
}
func TestWorkspaceDiffHelperA01ExpiredAfterMetadata(t *testing.T) {
	ctx := celltest.Context(context.Background())
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "expiry.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	defer db.Close(ctx)
	celltest.Create(t, db, "saved-tests", "saved", "example.test/saved")
	f, err := workspacedifftest.Build(ctx, db, t.TempDir())
	require.NoError(t, err)
	rt := &workspaceExpiryRuntime{Runtime: db}
	api := &workspaceExpiryAPI{Service: &jobinspect.Service{Runtime: rt, Tenant: f.Job.TenantId}, runtime: rt}
	server, err := Start(jobinspect.WithAPI(ctx, api), Options{Current: jobcontext.Current{TenantID: f.Job.TenantId, JobID: f.Job.JobId, GoalID: "saved"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer server.Close()
	broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, err)
	m, body, err := (JobClient{Broker: broker}).OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId})
	require.NoError(t, err)
	require.Equal(t, "unavailable", m.Status)
	require.Nil(t, m.Source)
	require.Nil(t, m.Bytes)
	require.Nil(t, body)
	require.NotEmpty(t, m.Reasons)
}

// A valid empty response for a different boundary must never become a false
// success, even when the original request omitted its default-after selector.
func TestWorkspaceDiffHelperReviewResponseContract(t *testing.T) {
	for _, variant := range []string{"malformed", "envelope-version", "before-default", "before-explicit", "after-default", "before-requested", "trailing-json"} {
		t.Run(variant, func(t *testing.T) {
			q := jobinspect.WorkspaceDiffRequest{JobID: "job"}
			boundary := "before"
			if variant == "before-requested" {
				q.Boundary = &boundary
				q.Task = func() *string { s := "task"; return &s }()
			}
			if variant == "before-explicit" {
				b := "after"
				q.Boundary = &b
				q.Task = func() *string { s := "task"; return &s }()
			}
			if variant == "after-default" {
				boundary = "after"
			}
			n := int64(0)
			m := &jobinspect.WorkspaceDiff{Version: 1, Status: "empty", Requested: q, Observation: "observed", Cutoff: "cut", Bytes: &n,
				Resolved:      jobinspect.WorkspaceDiffPoint{Status: "succeeded", Boundary: boundary},
				StateProducer: &jobinspect.WorkspaceDiffProducer{JobID: "job", Task: "task", Attempt: 1},
			}
			if q.Task != nil {
				m.Resolved.Task = *q.Task
			}
			// Empty-base endpoints are explicit and need no saved artifact.
			raw := `{"repository":"example.test/repo","empty_base":true}`
			require.NoError(t, json.Unmarshal([]byte(raw), &m.Workspace))
			var downloads atomic.Int32
			helper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
				require.Equal(t, "session", r.Header.Get(sessionHeader))
				if strings.HasSuffix(r.URL.Path, "/download") {
					downloads.Add(1)
					t.Error("unexpected download")
					return
				}
				if variant == "malformed" {
					io.WriteString(w, `{"version":`)
					return
				}
				version := 1
				if variant == "envelope-version" {
					version = 2
				}
				require.NoError(t, json.NewEncoder(w).Encode(jobinspect.Response{Version: version, WorkspaceDiff: m}))
				if variant == "trailing-json" {
					io.WriteString(w, `{}`)
				}
			}))
			defer helper.Close()
			c := JobClient{Broker: jobcontext.ChildJobBroker{Endpoint: helper.URL, Token: "token", SessionID: "session"}}
			success := variant == "after-default" || variant == "before-requested"
			_, err := c.Inspect(context.Background(), jobinspect.Request{Operation: "workspace_diff", JobID: q.JobID, WorkspaceDiff: &q.WorkspaceDiffOptions})
			if success {
				require.NoError(t, err)
			} else {
				var typed *jobinspect.WorkspaceDiffError
				require.ErrorAs(t, err, &typed)
				require.Equal(t, "protocol", typed.Code)
			}
			got, body, err := c.OpenWorkspaceDiff(context.Background(), q)
			if success {
				require.NoError(t, err)
				require.Equal(t, "empty", got.Status)
				require.NoError(t, body.Close())
			} else {
				require.Nil(t, got)
				require.Nil(t, body)
				var typed *jobinspect.WorkspaceDiffError
				require.ErrorAs(t, err, &typed)
				require.Equal(t, "protocol", typed.Code)
			}
			require.Zero(t, downloads.Load())
		})
	}
}
