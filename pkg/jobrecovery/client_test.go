package jobrecovery_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/jobrecovery"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestExactJobRecoveryFailuresNeverFallback(t *testing.T) {
	for _, mode := range []string{"selected", "sharing-restart"} {
		for _, scenario := range []string{"unsupported", "denied", "conflict", "redirect", "wrong-repository", "wrong-tenant", "missing-cell-id", "malformed", "oversized", "lost-response"} {
			if mode == "sharing-restart" && scenario == "unsupported" {
				continue
			}
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				var paths []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					paths = append(paths, r.Method+" "+r.URL.Path)
					if r.Method == http.MethodGet {
						require.Equal(t, "/v1/capabilities/primary-goal-selection", r.URL.Path)
						_ = json.NewEncoder(w).Encode(map[string]bool{"supported": scenario != "unsupported"})
						return
					}
					want := "/v1/tenants/tenant/jobs/destination/primary-goal"
					if mode == "sharing-restart" {
						want = "/v1/tenants/tenant/jobs/destination/restart"
					}
					require.Equal(t, http.MethodPut, r.Method)
					require.Equal(t, want, r.URL.Path)
					switch scenario {
					case "denied":
						w.WriteHeader(403)
						_, _ = w.Write([]byte(`{"code":"primary_goal_selection_forbidden"}`))
						return
					case "conflict":
						w.WriteHeader(409)
						_, _ = w.Write([]byte(`{"code":"existing_job_mismatch","message":"frozen request differs"}`))
						return
					case "redirect":
						w.Header().Set("Location", "/must-not-follow")
						w.WriteHeader(307)
						return
					case "malformed":
						_, _ = w.Write([]byte(`{"jobKey":`))
						return
					case "oversized":
						_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
						return
					case "lost-response":
						conn, _, err := w.(http.Hijacker).Hijack()
						require.NoError(t, err)
						_ = conn.Close()
						return
					}
					receipt := jobdb.JobHandle{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "destination"}, GoalID: "goal", Cell: "app", CellID: "captured-id", CellRepo: "example.test/app"}
					if scenario == "wrong-repository" {
						receipt.CellRepo = "example.test/other"
					}
					if scenario == "wrong-tenant" {
						receipt.JobKey.TenantId = "other"
					}
					if scenario == "missing-cell-id" {
						receipt.CellID = ""
					}
					_ = json.NewEncoder(w).Encode(receipt)
				}))
				defer server.Close()
				client, err := jobrecovery.New(server.URL, server.Client())
				require.NoError(t, err)
				job := jobdb.SubmitJob{TenantId: "tenant", JobID: "destination", GoalID: "goal", Cell: "app", CellRepo: "example.test/app", JobType: "work", Data: jobdb.NewTaskDataOrPanic(nil)}
				restart := jobdb.SubmitRestartJobRequest{RequestID: "original", Job: jobdb.SubmitRestartJob{PriorJobKey: jobdb.JobKey{TenantId: "tenant", JobId: "source"}, JobID: "destination", Cell: "app", CellRepo: "example.test/app", ShareGoal: true}}
				if mode == "selected" {
					_, err = client.ReplaySubmitJobRef(context.Background(), job)
				} else {
					_, err = client.ReplayRestartJobRef(context.Background(), restart)
				}
				require.Error(t, err)
				if scenario == "conflict" {
					require.ErrorIs(t, err, jobdb.ErrExistingJobMismatch)
				}
				writes := 0
				for _, path := range paths {
					if strings.HasPrefix(path, "PUT ") {
						writes++
					}
				}
				if scenario == "unsupported" {
					require.Zero(t, writes)
					require.Len(t, paths, 1)
				} else if mode == "selected" {
					require.Len(t, paths, 2)
					require.Equal(t, 1, writes)
				} else {
					require.Len(t, paths, 1)
					require.Equal(t, 1, writes)
				}
			})
		}
	}
}

func TestExactJobRecoveryRejectsUnfrozenRequestsAndCancellation(t *testing.T) {
	calls := 0
	client, err := jobrecovery.New("http://unused.invalid", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected HTTP") })})
	require.NoError(t, err)
	_, err = client.ReplaySubmitJobRef(context.Background(), jobdb.SubmitJob{})
	require.Error(t, err)
	_, err = client.ReplaySubmitJobRef(context.Background(), jobdb.SubmitJob{TenantId: "tenant", GoalID: "goal"})
	require.Error(t, err)
	for _, request := range []jobdb.SubmitRestartJobRequest{{}, {RequestID: "id"}, {Job: jobdb.SubmitRestartJob{ShareGoal: true}}} {
		_, err = client.ReplayRestartJobRef(context.Background(), request)
		require.Error(t, err)
	}
	require.Zero(t, calls)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	realClient, err := jobrecovery.New("http://127.0.0.1:1", nil)
	require.NoError(t, err)
	_, err = realClient.ReplaySubmitJobRef(ctx, jobdb.SubmitJob{TenantId: "tenant", JobID: "destination", GoalID: "goal", Cell: "app", JobType: "work", Data: jobdb.NewTaskDataOrPanic(nil)})
	require.ErrorIs(t, err, context.Canceled)
}
