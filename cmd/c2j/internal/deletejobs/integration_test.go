package deletejobs

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

type deletionFixture struct {
	uri, runtimeURL, tenant string
	calls                   atomic.Int64
}

func newDeletionFixture(t *testing.T, backend string) *deletionFixture {
	t.Helper()
	f := &deletionFixture{uri: "embed:///", runtimeURL: "embed:///", tenant: "0"}
	if backend == "embed" {
		t.Setenv("HOME", t.TempDir())
		return f
	}
	rt, err := sqlite.NewFromConfig(context.Background(), sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobdb.sqlite")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rt.Close(context.Background())) })
	server := remote.NewServer(rt)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		server.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	f.tenant = "tenant"
	f.runtimeURL = httpServer.URL
	f.uri = httpServer.URL + "/tenant"
	return f
}

func (f *deletionFixture) open(t *testing.T) *swfruntime.Handle {
	t.Helper()
	h, err := swfruntime.Open(context.Background(), f.runtimeURL)
	require.NoError(t, err)
	return h
}

func (f *deletionFixture) run(t *testing.T, opts Options) Result {
	t.Helper()
	var out bytes.Buffer
	opts.JobDBURI, opts.Stdout, opts.JSONOutput = f.uri, &out, true
	before := f.calls.Load()
	require.NoError(t, Run(context.Background(), opts))
	if f.runtimeURL != "embed:///" {
		require.EqualValues(t, 1, f.calls.Load()-before, "one bulk call; no listing, polling, or per-ID calls")
	}
	var result Result
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	return result
}

func TestDeletionSelectionAcrossRuntimes(t *testing.T) {
	for _, backend := range []string{"embed", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			f := newDeletionFixture(t, backend)
			h := f.open(t)
			for _, row := range []struct{ tenant, id, repo, recipe string }{
				{f.tenant, "backend", "backend", "build"}, {f.tenant, "frontend", "frontend", "build"},
				{f.tenant, "wrong-cell", "other", "build"}, {f.tenant, "wrong-recipe", "backend", "test"},
				{f.tenant, "excluded-id", "backend", "build"}, {"other-tenant", "backend", "backend", "build"},
			} {
				metadata, err := json.Marshal(map[string]string{"repo": "https://github.com/acme/" + row.repo + ".git", "recipe": row.recipe})
				require.NoError(t, err)
				_, err = h.Runtime.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{
					TenantId: row.tenant, JobID: row.id, JobType: "test", Metadata: metadata, Data: jobdb.NewTaskDataOrPanic("input"),
				}})
				require.NoError(t, err)
			}
			key := jobdb.JobKey{TenantId: f.tenant, JobId: "backend"}
			listed, err := h.Runtime.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{f.tenant}, JobKeys: []jobdb.JobKey{key}})
			require.NoError(t, err)
			require.Len(t, listed.Jobs, 1)
			created := listed.Jobs[0].CreatedAt
			lease, err := h.Runtime.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: key, Routes: []jobdb.Route{{JobType: "test"}}, WorkerID: "test", LeaseDuration: time.Hour})
			require.NoError(t, err)
			require.NotNil(t, lease)
			require.NoError(t, h.Cleanup())

			result := f.run(t, Options{JobIDs: []string{"backend"}, CreatedBefore: created.Format(time.RFC3339Nano), DryRun: true})
			require.Zero(t, result.MatchedCount, "creation cutoff is exclusive")
			result = f.run(t, Options{JobIDs: []string{"backend"}, CreatedBefore: created.Add(time.Nanosecond).Format(time.RFC3339Nano), DryRun: true})
			require.EqualValues(t, 1, result.MatchedCount)
			result = f.run(t, Options{All: true, DryRun: true})
			require.EqualValues(t, 5, result.MatchedCount)
			opts := Options{WorkingDir: cellConfig(t), Cells: []string{"backend", "frontend"}, Recipe: "build",
				JobIDs:        []string{"backend", "frontend", "wrong-cell", "wrong-recipe", "missing"},
				CreatedBefore: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), DryRun: true}
			result = f.run(t, opts)
			require.EqualValues(t, 2, result.MatchedCount)
			require.Zero(t, result.DeletedCount)
			if backend == "remote" {
				require.NoError(t, lease.KeepAlive(ctx))
			}
			opts.DryRun = false
			result = f.run(t, opts)
			require.EqualValues(t, 2, result.DeletedCount)
			require.EqualValues(t, 2, result.MatchedCount)
			if backend == "remote" {
				require.Error(t, lease.KeepAlive(ctx))
				require.Error(t, lease.Complete(ctx, jobdb.CompleteExecutionRequest{Status: "success"}))
			}
			result = f.run(t, opts)
			require.Zero(t, result.MatchedCount, "already hidden and missing IDs do not count")
			h = f.open(t)
			listed, err = h.Runtime.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{f.tenant}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}})
			require.NoError(t, err)
			require.Len(t, listed.Jobs, 3)
			info, err := h.Runtime.GetJob(ctx, key)
			require.NoError(t, err, "direct lookup is retained")
			require.Equal(t, jobdb.JobStatusCancelled, info.Status)
			newLease, err := h.Runtime.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: key, Routes: []jobdb.Route{{JobType: "test"}}, WorkerID: "test", LeaseDuration: time.Hour})
			require.NoError(t, err)
			require.Nil(t, newLease)
			listed, err = h.Runtime.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"other-tenant"}})
			require.NoError(t, err)
			require.Len(t, listed.Jobs, 1)
			require.NoError(t, h.Cleanup())
			result = f.run(t, Options{All: true})
			require.EqualValues(t, 3, result.DeletedCount)
		})
	}
}

func TestDeletionErrorsDoNotFallBack(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusNotImplemented, jobdb.ErrJobDeletionUnsupported},
		{http.StatusBadRequest, jobdb.ErrInvalidJobDeletion},
		{http.StatusServiceUnavailable, nil},
	} {
		var calls atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Method != http.MethodPost || r.URL.Path != "/v1/tenants/tenant/jobs/delete" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, `{"error":"unavailable"}`)
		}))
		err := Run(context.Background(), Options{JobDBURI: server.URL + "/tenant", All: true, Stdout: io.Discard})
		server.Close()
		require.Error(t, err)
		if tc.want != nil {
			require.ErrorIs(t, err, tc.want)
		}
		require.EqualValues(t, 1, calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, Options{JobDBURI: "http://127.0.0.1:1/tenant", All: true, Stdout: io.Discard})
	require.ErrorIs(t, err, context.Canceled)
}
