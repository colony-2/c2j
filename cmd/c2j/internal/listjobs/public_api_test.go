package listjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/stretchr/testify/require"
)

type parityRuntime struct {
	jobdb.WorkflowRuntime
	jobdb.ConfigurationAdmin
	job      jobdb.JobSummary
	requests chan jobdb.ListJobsRequest
}

func (r *parityRuntime) ListJobs(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	r.requests <- req
	return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{r.job}, NextPageToken: "opaque-token"}, nil
}

func TestPublicAPIAndCLIParity(t *testing.T) {
	for _, status := range []jobdb.JobStatus{jobdb.JobStatusReady, jobdb.JobStatusCrashConcern, jobdb.JobStatusActive, jobdb.JobStatusCompleted} {
		t.Run(string(status), func(t *testing.T) {
			memory := "8Gi"
			demand, err := execution.Initial(nil, true, execution.Requirements{Resources: execution.Resources{Memory: &memory}})
			require.NoError(t, err)
			meta, err := json.Marshal(map[string]any{"repo": "https://github.com/acme/app.git", "execution": demand})
			require.NoError(t, err)
			runtime := &parityRuntime{requests: make(chan jobdb.ListJobsRequest, 2), job: jobdb.JobSummary{GoalID: "goal", OtherGoalIDs: []string{},
				JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, Status: status, JobType: "recipe", Metadata: meta,
				CreatedAt: time.Unix(10, 0).UTC(), AvailableAt: time.Unix(20, 0).UTC(), CancelRequested: true,
				NextRoute:        &jobdb.Route{JobType: "recipe", TaskType: "recipe:two-step:next"},
				CompletionDetail: "  exact detail\n☃\n",
				ClientPayload:    json.RawMessage(`{"application":[9007199254740993,null],"c2j":{"other":true}}`), ClientPayloadRevision: 7,
				WaitFor:        []string{"second", "first"},
				ExecutionState: jobdb.ExecutionState{TaskWait: &jobdb.TaskWait{InputOrdinal: 2, OutputOrdinal: 3, InputHash: "hash", ResumeJobType: "recipe"}},
			}}
			s := httptest.NewServer(remote.NewServer(runtime))
			defer s.Close()
			uri := s.URL + "/tenant"
			root := t.TempDir()
			writeListConfig(t, root, "self:\n  repo: github.com/acme/app\n")
			var out bytes.Buffer
			require.NoError(t, Run(context.Background(), Options{JobDBURI: uri, WorkingDir: root, Cell: "github.com/acme/app", Statuses: []string{string(status)}, JobTypes: []string{"recipe"}, PageSize: 1, PageToken: "previous", JSONOutput: true, Stdout: &out}))
			c, err := joblist.New(joblist.Config{JobDBURI: uri, HTTPClient: s.Client()})
			require.NoError(t, err)
			page, err := c.List(context.Background(), joblist.Query{Repository: "github.com/acme/app", Statuses: []jobdb.JobStatus{status}, JobTypes: []string{"recipe"}, PageSize: 1, PageToken: "previous"})
			require.NoError(t, err)
			var cliPage joblist.Page
			require.NoError(t, json.Unmarshal(out.Bytes(), &cliPage))
			require.Equal(t, cliPage, page)
			require.Equal(t, "opaque-token", cliPage.NextPageToken)
			require.Len(t, cliPage.Jobs, 1)
			require.Equal(t, runtime.job.ClientPayload, cliPage.Jobs[0].ClientPayload)
			require.Equal(t, runtime.job.ClientPayloadRevision, cliPage.Jobs[0].ClientPayloadRevision)
			require.Equal(t, runtime.job.CompletionDetail, cliPage.Jobs[0].CompletionDetail)
			require.Equal(t, runtime.job.WaitFor, cliPage.Jobs[0].WaitFor)
			require.Equal(t, runtime.job.ExecutionState.TaskWait, cliPage.Jobs[0].TaskWait)
			require.Equal(t, <-runtime.requests, <-runtime.requests, "CLI and public API must send equivalent backend queries")
		})
	}
}
