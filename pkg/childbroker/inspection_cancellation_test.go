package childbroker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestJobInspectionCancelledAdmission(t *testing.T) {
	routes := []struct {
		name, suffix, payload string
		typed                 bool
		family                bool
	}{
		{name: "show", payload: `{"operation":"show","job_id":"other"}`},
		{name: "metrics", payload: `{"operation":"metrics","job_id":"other","metrics":{}}`},
		{name: "family", payload: `{"operation":"family-node","job_id":"other","family":{"version":1}}`, family: true},
		{name: "workspace", payload: `{"operation":"workspace_diff","job_id":"other"}`, typed: true},
		{name: "named-download", suffix: "/download", payload: `{"job_id":"other","name":"file"}`},
		{name: "exact-download", suffix: "/download", payload: `{"operation":"artifact","job_id":"other","reference":{"jobId":"other","name":"diff","taskOrdinal":0,"sizeBytes":5}}`, typed: true},
	}
	for _, route := range routes {
		for _, source := range []string{"execution", "request", "permission"} {
			for _, failure := range []struct {
				name string
				err  error
			}{
				{name: "canceled", err: context.Canceled},
				{name: "timeout", err: context.DeadlineExceeded},
				{name: "lease_lost", err: jobdb.ErrExecutionLeaseLost},
			} {
				t.Run(route.name+"/"+source+"/"+failure.name, func(t *testing.T) {
					var expired context.Context
					if failure.err == context.DeadlineExceeded {
						var cancel context.CancelFunc
						expired, cancel = context.WithDeadline(context.Background(), time.Unix(0, 0))
						defer cancel()
					} else {
						ctx, cancel := context.WithCancelCause(context.Background())
						cancel(failure.err)
						expired = ctx
					}
					execution, request := context.Background(), context.Background()
					switch source {
					case "execution":
						execution = expired
					case "request":
						request = expired
					case "permission":
						execution = permissions.WithAccess(execution, permissions.NewAccess(expired, permissions.JobDB{}, nil, nil))
					}
					probe := &inspectionProbe{}
					s := &Server{executionContext: jobinspect.WithAPI(execution, probe), token: "token", sessionID: "session"}
					r := httptest.NewRequest(http.MethodPost, submitPath+"/inspect"+route.suffix, strings.NewReader(route.payload)).WithContext(request)
					r.Header.Set("Authorization", "Bearer token")
					r.Header.Set(sessionHeader, "session")
					w := httptest.NewRecorder()
					// Invoke the authenticated handler without Start's asynchronous
					// listener shutdown. The backend deliberately ignores context.
					s.handleJobInspection(w, r)
					require.Zero(t, probe.calls.Load(), "expired scope must not enter either backend method")
					require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
					if route.family {
						require.Equal(t, http.StatusConflict, w.Code)
						var diagnostic jobdb.CellError
						require.NoError(t, json.Unmarshal(w.Body.Bytes(), &diagnostic))
						require.Equal(t, failure.name, diagnostic.Code)
					} else {
						require.Equal(t, http.StatusBadRequest, w.Code)
						if route.typed {
							var diagnostic jobinspect.WorkspaceDiffError
							require.NoError(t, json.Unmarshal(w.Body.Bytes(), &diagnostic))
							require.Equal(t, jobinspect.WorkspaceDiffVersion, diagnostic.Version)
							require.Equal(t, failure.name, diagnostic.Code)
						} else {
							require.Equal(t, failure.err.Error()+"\n", w.Body.String())
						}
					}
				})
			}
		}
	}
}
