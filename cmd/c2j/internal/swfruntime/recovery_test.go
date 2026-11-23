package swfruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestExactJobRemoteOwnerRecoveryWiring(t *testing.T) {
	t.Setenv("JOBDB_BEARER_TOKEN", "fixture-owner")
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer fixture-owner", r.Header.Get("Authorization"))
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			require.Equal(t, "/v1/capabilities/primary-goal-selection", r.URL.Path)
			_, _ = w.Write([]byte(`{"supported":true}`))
			return
		}
		require.Equal(t, http.MethodPut, r.Method)
		_ = json.NewEncoder(w).Encode(jobdb.JobHandle{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "destination"}, GoalID: "retained-goal", Cell: "app", CellID: "captured-cell", CellRepo: "example.test/app"})
	}))
	defer server.Close()
	handle, err := Open(context.Background(), server.URL)
	require.NoError(t, err)
	defer handle.Cleanup()
	submitter, ok := handle.Engine.(jobref.Submitter)
	require.True(t, ok)
	descriptor := starter.Descriptor{Version: 1, Operation: "submit", Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "destination", GoalID: "retained-goal", Cell: "app", CellRepo: "example.test/app", JobType: "work"}, Input: json.RawMessage(`null`)}
	ref, err := descriptor.Submit(context.Background(), submitter)
	require.NoError(t, err)
	require.Equal(t, "destination", ref.JobID)
	restart, ok := handle.Engine.(jobref.RestartSubmitter)
	require.True(t, ok)
	_, err = starter.ReplayRestartJob(context.Background(), restart, jobdb.SubmitRestartJobRequest{RequestID: "original-id", Job: jobdb.SubmitRestartJob{PriorJobKey: jobdb.JobKey{TenantId: "tenant", JobId: "source"}, JobID: "destination", Cell: "app", CellRepo: "example.test/app", ShareGoal: true}})
	require.NoError(t, err)
	require.Equal(t, []string{"GET /v1/capabilities/primary-goal-selection", "PUT /v1/tenants/tenant/jobs/destination/primary-goal", "PUT /v1/tenants/tenant/jobs/destination/restart"}, paths)
}
