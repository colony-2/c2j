package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/cmd/c2j/internal/buildinfo"
	"github.com/colony-2/c2j/cmd/c2j/internal/deletejobs"
	"github.com/stretchr/testify/require"
)

func TestDeleteCommandMakesOneBulkRequest(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/tenants/tenant/jobs/delete" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			JobIDs []string `json:"jobIds"`
			DryRun bool     `json:"dryRun"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.JobIDs) != 2 || body.JobIDs[0] != "a,b" || body.JobIDs[1] != "second" || !body.DryRun {
			t.Errorf("unexpected selection: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"matchedCount":2,"deletedCount":0}`))
	}))
	defer server.Close()
	root := newRootCmd(buildinfo.Info{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"delete", "--jobdb", server.URL + "/tenant", "--job-id", "a,b", "--job-id", "second", "--dry-run", "--json"})
	require.NoError(t, root.Execute())
	require.EqualValues(t, 1, calls.Load())
	var result deletejobs.Result
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	require.Equal(t, "tenant", result.TenantID)
	require.True(t, result.DryRun)
	require.EqualValues(t, 2, result.MatchedCount)
	require.Zero(t, result.DeletedCount)
}

func TestDeleteFlagsPreserveIdentifiers(t *testing.T) {
	cmd := newDeleteCmd()
	require.NoError(t, cmd.Flags().Parse([]string{"--cell", "backend", "--cell", "frontend", "--job-id", "a,b", "--job-id", "other"}))
	cells, err := cmd.Flags().GetStringArray("cell")
	require.NoError(t, err)
	require.Equal(t, []string{"backend", "frontend"}, cells)
	ids, err := cmd.Flags().GetStringArray("job-id")
	require.NoError(t, err)
	require.Equal(t, []string{"a,b", "other"}, ids)
}

func TestDeleteRejectsUnsupportedAndEmptyFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--status", "COMPLETED"}, {"--page-size", "10"}, {"unexpected-positional-id"},
		{"--all", "--recipe", ""}, {"--all", "--older-than", ""}, {"--all", "--created-before", ""},
	} {
		root := newRootCmd(buildinfo.Info{})
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{"delete"}, args...))
		require.Error(t, root.Execute(), "%v", args)
	}
}
