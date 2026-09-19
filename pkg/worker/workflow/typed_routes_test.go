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
	task    jobworkflow.TaskHandle
}

func (e waitingTaskEngine) ListJobs(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{e.summary}}, nil
}
func (e waitingTaskEngine) GetWaitingTask(context.Context, jobdb.JobKey) (jobworkflow.TaskHandle, error) {
	return e.task, nil
}

type waitingTaskHandle struct {
	jobworkflow.TaskHandle
	ordinal int64
}

func (h waitingTaskHandle) TaskOrdinalToComplete() int64 { return h.ordinal }
func (h waitingTaskHandle) Data() (jobdb.TaskData, error) {
	return jobdb.NewTaskDataOrPanic(map[string]bool{"prompt": true}), nil
}

func TestListedTaskUsesExplicitOutputCoordinate(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	summary := jobdb.JobSummary{
		JobKey: key, NextRoute: &jobdb.Route{JobType: "recipe:kind", TaskType: "input:collect"},
		ExecutionState: jobdb.ExecutionState{TaskWait: &jobdb.TaskWait{
			InputOrdinal: 0, OutputOrdinal: 9, InputHash: "hash", ResumeJobType: "recipe:kind",
		}},
	}
	for _, ordinal := range []int64{9, 10} {
		control := &SWFWorkflowControl{Engine: waitingTaskEngine{summary: summary, task: waitingTaskHandle{ordinal: ordinal}}}
		jobs, _, err := control.ListJobs(context.Background(), jobdb.ListJobsRequest{})
		require.NoError(t, err)
		require.Len(t, jobs, 1)
		data, err := jobs[0].TaskData.GetData()
		if ordinal == 9 {
			require.NoError(t, err)
			require.JSONEq(t, `{"prompt":true}`, string(data))
		} else {
			require.ErrorContains(t, err, "unexpected task ordinal: 9")
		}
	}
}
