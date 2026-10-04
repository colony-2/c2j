package listjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/colony-2/c2j/cmd/c2j/internal/childjobs"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/stretchr/testify/require"
)

func TestCompletionFieldsInOrdinaryAndChildCLIJSON(t *testing.T) {
	for _, completion := range []string{"success", "failed_app", "failed_system", "failed_timeout", "cancelled", "future_category", ""} {
		t.Run(completion, func(t *testing.T) {
			status := jobdb.JobStatusCompleted
			detail := "  original detail\n☃\n"
			if completion == "" {
				status = jobdb.JobStatusReady
				detail = ""
			}
			if completion == "success" {
				detail = ""
			}
			if completion == "cancelled" {
				status = jobdb.JobStatusCancelled
			}
			rt := &parityRuntime{requests: make(chan jobdb.ListJobsRequest, 2), job: jobdb.JobSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "child"}, JobType: "recipe", Status: status, CompletionStatus: completion, CompletionDetail: detail, ParentJobID: "parent", Metadata: json.RawMessage(`{"recipe":"not-installed","repo":"https://github.com/acme/app.git"}`)}}
			server := httptest.NewServer(remote.NewServer(rt))
			defer server.Close()
			root := t.TempDir()
			writeListConfig(t, root, "self:\n  repo: github.com/acme/app\n")
			var ordinary, children bytes.Buffer
			require.NoError(t, Run(context.Background(), Options{JobDBURI: server.URL + "/tenant", WorkingDir: root, Cell: "github.com/acme/app", Statuses: []string{string(status)}, PageSize: 1, PageToken: "previous", JSONOutput: true, Stdout: &ordinary}))
			require.NoError(t, childjobs.Run(context.Background(), childjobs.Options{JobDBURI: server.URL + "/tenant", WorkingDir: root, ParentTenantID: "tenant", ParentJobID: "parent", AllParentInvocations: true, Statuses: []string{string(status)}, PageSize: 1, PageToken: "previous", JSONOutput: true, Stdout: &children}))
			for _, out := range []*bytes.Buffer{&ordinary, &children} {
				var page struct {
					Jobs []map[string]any `json:"jobs"`
					Next string           `json:"next_page_token"`
				}
				require.NoError(t, json.Unmarshal(out.Bytes(), &page))
				require.Len(t, page.Jobs, 1)
				require.Equal(t, "opaque-token", page.Next)
				got := page.Jobs[0]
				require.Equal(t, string(status), got["status"])
				if completion == "" {
					require.NotContains(t, got, "completion_status")
				} else {
					require.Equal(t, completion, got["completion_status"])
				}
				if detail == "" {
					require.NotContains(t, got, "completion_detail")
				} else {
					require.Equal(t, detail, got["completion_detail"])
				}
			}
			normalReq, childReq := <-rt.requests, <-rt.requests
			require.Equal(t, []jobdb.JobStatus{status}, normalReq.Statuses)
			require.Equal(t, normalReq.Statuses, childReq.Statuses)
			require.Equal(t, "previous", normalReq.PageToken)
			require.Equal(t, "previous", childReq.PageToken)
			require.Equal(t, []string{"parent"}, childReq.ParentJobIDs)
		})
	}
}
