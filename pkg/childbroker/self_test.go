package childbroker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/stretchr/testify/require"
)

func TestInspectWithoutSubmitPermission(t *testing.T) {
	current := jobcontext.Current{TenantID: "tenant", JobID: "job", GoalID: "goal"}
	redactor := secrets.NewRedactor()
	require.NoError(t, redactor.Register("private-value"))
	var calls atomic.Int32
	server, err := Start(secrets.WithRedactor(context.Background(), redactor), Options{Current: current, Self: func() selfinfo.Info {
		calls.Add(1)
		i := selfinfo.Unknown("")
		i.SetCurrent(current, "runtime_helper")
		i.Execution.RecipeSource = selfinfo.String("https://user:password@example.test/repo?token=private-value")
		i.Permissions.JobDB = &permissions.JobDB{}
		i.Permissions.Status = "known"
		i.Lease.Status = "active"
		i.SetCapabilities(false, false, false)
		return i
	}})
	require.NoError(t, err)
	defer server.Close()
	broker, ok, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, err)
	require.True(t, ok)
	broker.Endpoint = server.localEndpoint
	info, err := Inspect(context.Background(), broker)
	require.NoError(t, err)
	require.Equal(t, "denied", info.Capabilities["jobs.submit"].Permission)
	require.Equal(t, "https://example.test/repo", selfinfo.Value(info.Execution.RecipeSource))
	require.Equal(t, int32(1), calls.Load())
	broker.Token = "wrong-private-value"
	_, err = Inspect(context.Background(), broker)
	require.ErrorContains(t, err, "helper_authentication_failed")
	require.NotContains(t, err.Error(), "private-value")
	require.Equal(t, int32(1), calls.Load(), "unauthenticated inspection cannot reach provider")
	req, err := http.NewRequest(http.MethodPost, server.localEndpoint+"/self", nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)
}
func TestInspectFailureModesAndNoRedirect(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		code    string
	}{
		{"old", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, "inspection_unsupported"},
		{"large", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, strings.Repeat("x", selfinfo.MaxResponseBytes+1))
		}, "helper_response_too_large"},
		{"invalid", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "private-token malformed") }, "invalid_helper_response"},
		{"redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }, "helper_unavailable"},
		{"timeout", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, "helper_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := Inspect(ctx, jobcontext.ChildJobBroker{Endpoint: server.URL, Token: "private-token", SessionID: "session"})
			require.ErrorContains(t, err, tc.code)
			require.NotContains(t, err.Error(), "private-token")
			require.NotContains(t, err.Error(), server.URL)
		})
	}
	require.Zero(t, redirected.Load())
}
