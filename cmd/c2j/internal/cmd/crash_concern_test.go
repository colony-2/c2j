package cmd

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/colony-2/c2j/internal/buildinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/stretchr/testify/require"
)

type crashConcernRuntime struct {
	jobdb.WorkflowRuntime
	jobdb.ConfigurationAdmin
	req   jobdb.ClearCrashConcernRequest
	err   error
	calls int
}

func (r *crashConcernRuntime) ClearCrashConcern(_ context.Context, req jobdb.ClearCrashConcernRequest) error {
	r.calls++
	r.req = req
	return r.err
}

func TestClearCrashConcernCLI(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	rt := &crashConcernRuntime{}
	handler := remote.NewServer(rt)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer operator-token", r.Header.Get("Authorization"))
		if rt.err == jobdb.ErrLeasePermissionDenied {
			http.Error(w, "operator access denied", http.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv("JOBDB_BEARER_TOKEN", "operator-token")
	t.Setenv("C2J_JOBDB", server.URL+"/configured")
	run := func(args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		root := newRootCmd(buildinfo.Info{})
		root.SetOut(&out)
		root.SetErr(io.Discard)
		root.SetArgs(args)
		err := root.Execute()
		return out.String(), err
	}
	out, err := run("job", "clear-crash-concern", "job", "--reason", "worker repaired", "--worker-id", "operator", "--json")
	require.NoError(t, err)
	require.JSONEq(t, `{"tenant_id":"configured","job_id":"job","cleared":true}`, out)
	require.Equal(t, jobdb.ClearCrashConcernRequest{JobKey: jobdb.JobKey{TenantId: "configured", JobId: "job"}, WorkerID: "operator", Reason: "worker repaired"}, rt.req)
	require.Equal(t, 1, rt.calls, "the command only clears; it does not claim, list, or restart jobs")
	out, err = run("job", "--jobdb", server.URL+"/explicit", "reset-crash-concern", "another")
	require.NoError(t, err)
	require.Equal(t, "Cleared crash concern for job explicit/another.\n", out)
	require.Equal(t, jobdb.ClearCrashConcernRequest{JobKey: jobdb.JobKey{TenantId: "explicit", JobId: "another"}, WorkerID: "c2j-cli"}, rt.req)
	for _, want := range []error{jobdb.ErrJobNotFound, jobdb.ErrConflict, jobdb.ErrLeasePermissionDenied} {
		rt.err = want
		out, err = run("job", "clear-crash-concern", "job", "--json")
		require.ErrorIs(t, err, want)
		require.Empty(t, out, "errors must not emit success JSON")
	}
	before := rt.calls
	for _, args := range [][]string{
		{"job", "clear-crash-concern"},
		{"job", "clear-crash-concern", "one", "two"},
		{"job", "clear-crash-concern", " "},
		{"job", "clear-crash-concern", "job", "--jobdb", "invalid://target"},
	} {
		_, err := run(args...)
		require.Error(t, err)
	}
	require.Equal(t, before, rt.calls, "invalid requests must not reach JobDB")
}
