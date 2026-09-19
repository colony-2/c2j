package recipejob

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"strconv"
	"testing"
)

type pagedExecutionLister struct {
	jobs  []jobdb.JobSummary
	calls int
}

func (l *pagedExecutionLister) ListJobs(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	l.calls++
	start, _ := strconv.Atoi(req.PageToken)
	end := start + req.PageSize
	if end > len(l.jobs) {
		end = len(l.jobs)
	}
	resp := jobdb.ListJobsResponse{Jobs: l.jobs[start:end]}
	if end < len(l.jobs) {
		resp.NextPageToken = strconv.Itoa(end)
	}
	return resp, nil
}
func summaryWithMemory(t *testing.T, id, memory string) jobdb.JobSummary {
	d, err := execution.Initial(nil, "pinned", execution.Requirements{Resources: execution.Resources{Memory: &memory}})
	require.NoError(t, err)
	meta, err := json.Marshal(map[string]any{"v": 1, "recipe": "test", "execution": d})
	require.NoError(t, err)
	return jobdb.JobSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: id}, JobType: "recipe", Metadata: meta}
}
func TestExecutionFilteringFillsPagesWithoutSkippingMatches(t *testing.T) {
	l := &pagedExecutionLister{jobs: []jobdb.JobSummary{summaryWithMemory(t, "1", "8Gi"), summaryWithMemory(t, "2", "2Gi"), summaryWithMemory(t, "3", "8Gi"), summaryWithMemory(t, "4", "2Gi"), summaryWithMemory(t, "5", "2Gi")}}
	memory := "4Gi"
	filter := &execution.Filter{Allocation: execution.Allocation{SchemaVersion: 1, Resources: execution.Resources{Memory: &memory}}}
	req := jobdb.ListJobsRequest{PageSize: 2}
	first, err := ListExecutionJobs(context.Background(), l, req, filter)
	require.NoError(t, err)
	require.Len(t, first.Jobs, 2)
	require.Equal(t, "2", first.Jobs[0].JobKey.JobId)
	require.Equal(t, "4", first.Jobs[1].JobKey.JobId)
	require.Greater(t, l.calls, 1)
	req.PageToken = first.NextPageToken
	second, err := ListExecutionJobs(context.Background(), l, req, filter)
	require.NoError(t, err)
	require.Len(t, second.Jobs, 1)
	require.Equal(t, "5", second.Jobs[0].JobKey.JobId)
	require.Empty(t, second.NextPageToken)
	l.jobs = []jobdb.JobSummary{{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "bad"}, ClientPayload: json.RawMessage(`{"c2j":{"execution":{"schema_version":99}}}`)}}
	req.PageToken = ""
	_, err = ListExecutionJobs(context.Background(), l, req, filter)
	var demandErr *ExecutionDemandError
	require.ErrorAs(t, err, &demandErr)
	require.Equal(t, "bad", demandErr.JobKey.JobId)
}
