package consumerfixture_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/consumerfixture"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	sqlitesecrets "github.com/colony-2/jobdb/pkg/jobdb/secrets/sqlite"
	"github.com/stretchr/testify/require"
)

func consumerSecretHandler(t *testing.T, rt jobdb.ConsumerRuntime, constructor string) http.Handler {
	t.Helper()
	if constructor == "options" {
		handler, err := remote.NewConsumerServerWithOptions(rt, remote.ServerOptions{LeaseTokenSigningKey: []byte(strings.Repeat("k", 32))})
		require.NoError(t, err)
		return celltest.Handler(handler)
	}
	return celltest.Handler(remote.NewConsumerServer(rt))
}

// Exercise the genuine store through both registrars and compare the retained
// decorator with its backend. Resolving through a remote lease proves these
// routes still perform administration rather than merely returning success.
func TestC05RetainedDevSecretAdministration(t *testing.T) {
	for _, constructor := range []string{"default", "options"} {
		for _, decorator := range []string{"backend", "retained"} {
			t.Run(constructor+"/"+decorator, func(t *testing.T) {
				ctx := celltest.Context(context.Background())
				db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
				var rt jobdb.ConsumerRuntime = db
				if decorator == "retained" {
					rt = consumerfixture.New(db)
				}
				server := httptest.NewServer(consumerSecretHandler(t, rt, constructor))
				t.Cleanup(server.Close)
				client, err := remote.New(server.URL, server.Client())
				require.NoError(t, err)
				celltest.Create(t, client, "tenant", "test", "example.test/test")
				job, err := client.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "secrets", JobType: "work", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
				require.NoError(t, err)
				lease, err := client.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: job.JobKey, WorkerID: "worker", Routes: []jobdb.Route{{JobType: "work"}}, LeaseDuration: time.Minute})
				require.NoError(t, err)
				require.NotNil(t, lease)
				post := func(operation string, body any) {
					t.Helper()
					raw, err := json.Marshal(body)
					require.NoError(t, err)
					response, err := server.Client().Post(server.URL+"/dev/v1/tenants/tenant/secrets/"+operation, "application/json", strings.NewReader(string(raw)))
					require.NoError(t, err)
					defer response.Body.Close()
					data, err := io.ReadAll(response.Body)
					require.NoError(t, err)
					require.Equal(t, http.StatusNoContent, response.StatusCode, "%s: %s", operation, data)
					require.Empty(t, data)
					require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
				}
				for _, value := range []string{" café\r\n\t", ""} {
					post("set", map[string]string{"name": "token", "value": value})
					resolved, err := lease.ResolveSecrets(ctx, []string{"token"})
					require.NoError(t, err)
					require.Contains(t, resolved.Secrets, "token")
					require.Equal(t, value, resolved.Secrets["token"].Value)
				}
				post("remove", map[string]string{"name": "token"})
				resolved, err := lease.ResolveSecrets(ctx, []string{"token"})
				require.NoError(t, err)
				require.Empty(t, resolved.Secrets)
			})
		}
	}
}

func TestC05RetainedDevSecretErrors(t *testing.T) {
	ctx := celltest.Context(context.Background())
	provider, err := sqlitesecrets.Open(ctx, filepath.Join(t.TempDir(), "provider.db"), jobdb.SecretLimits{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, provider.Close()) })
	for _, configured := range []bool{true, false} {
		name := "configured"
		config := sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), SecretLimits: jobdb.SecretLimits{MaxResponseBytes: 128}, Projects: jobdbcelltest.UnrestrictedProjects{}}
		if !configured {
			name = "external-provider"
			config.SecretProvider = provider // No development store, as on the backend.
		}
		t.Run(name, func(t *testing.T) {
			db, err := sqlite.NewFromConfig(ctx, config)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
			retained := consumerfixture.New(db)
			admin, ok := any(retained).(interface {
				SetDevSecret(context.Context, string, string, string) error
				RemoveDevSecret(context.Context, string, string) error
			})
			require.True(t, ok, "development-secret administration must survive decoration")
			if configured {
				require.NoError(t, admin.SetDevSecret(ctx, "tenant", "token", "keep"))
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				// The store sanitizes cancellation to ErrSecretsUnavailable. Keep
				// the backend's error contract rather than inventing a new one.
				setErr := admin.SetDevSecret(cancelled, "tenant", "token", "changed")
				removeErr := admin.RemoveDevSecret(cancelled, "tenant", "token")
				require.ErrorIs(t, setErr, jobdb.ErrSecretsUnavailable)
				require.ErrorIs(t, setErr, db.SetDevSecret(cancelled, "tenant", "token", "changed"))
				require.ErrorIs(t, removeErr, jobdb.ErrSecretsUnavailable)
				require.ErrorIs(t, removeErr, db.RemoveDevSecret(cancelled, "tenant", "token"))
				require.ErrorIs(t, admin.SetDevSecret(ctx, "tenant", "token", strings.Repeat("x", 256)), jobdb.ErrSecretRequestTooLarge)
			} else {
				require.ErrorIs(t, admin.SetDevSecret(ctx, "tenant", "token", "value"), jobdb.ErrDevSecretsNotConfigured)
				require.ErrorIs(t, admin.RemoveDevSecret(ctx, "tenant", "token"), jobdb.ErrDevSecretsNotConfigured)
			}
			for _, constructor := range []string{"default", "options"} {
				for _, operation := range []string{"set", "remove"} {
					body := `{"name":"token"}`
					if operation == "set" {
						value := "value"
						if configured {
							value = strings.Repeat("x", 256)
						}
						body = `{"name":"token","value":"` + value + `"}`
					}
					// Compare the exact unsupported/limit response with the backend.
					request := func(rt jobdb.ConsumerRuntime) *httptest.ResponseRecorder {
						t.Helper()
						recorder := httptest.NewRecorder()
						consumerSecretHandler(t, rt, constructor).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/dev/v1/tenants/tenant/secrets/"+operation, strings.NewReader(body)))
						return recorder
					}
					control, got := request(db), request(retained)
					require.Equal(t, control.Code, got.Code)
					require.Equal(t, control.Body.String(), got.Body.String())
					want := http.StatusServiceUnavailable
					if configured {
						want = http.StatusNoContent
						if operation == "set" {
							want = http.StatusRequestEntityTooLarge
						}
					}
					require.Equal(t, want, got.Code)
				}
			}
		})
	}
}
