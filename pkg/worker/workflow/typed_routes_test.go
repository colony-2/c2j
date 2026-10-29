package workflow

import (
	"context"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type waitingTaskEngine struct {
	jobworkflow.Engine
	summary jobdb.JobSummary
}

func (e waitingTaskEngine) ListJobs(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{e.summary}}, nil
}
func TestListedTasksRequireExplicitDiscovery(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	summary := jobdb.JobSummary{JobKey: key, NextRoute: &jobdb.Route{JobType: "recipe:kind", TaskType: "input:collect"}, ExecutionState: jobdb.ExecutionState{RunPolicy: jobdb.DefaultRunPolicy(), TaskWait: &jobdb.TaskWait{OutputOrdinal: 9}}}
	// All methods except ListJobs are nil: unexpected detail reads panic.
	control := &SWFWorkflowControl{Engine: waitingTaskEngine{summary: summary}}
	jobs, _, err := control.ListJobs(context.Background(), jobdb.ListJobsRequest{})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Nil(t, jobs[0].TaskData)
	require.Equal(t, summary.ExecutionState, jobs[0].ExecutionState)
	require.Equal(t, summary.NextRoute, jobs[0].NextRoute)
}
