package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type rawInputEngine struct {
	jobworkflow.Engine
	requestedInput bool
}

func (e *rawInputEngine) ListJobs(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{{
		JobKey: req.JobKeys[0], JobType: "recipe", Status: jobdb.JobStatusReady,
		ClientPayload: json.RawMessage(`{"cursor":"not-job-input"}`),
	}}}, nil
}

func (e *rawInputEngine) GetJobRun(_ context.Context, req jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error) {
	e.requestedInput = req.IncludeInputs
	return jobdb.GetJobRunResponse{Start: jobdb.JobStart{Input: &jobdb.TaskIO{Data: json.RawMessage(`{"recipe":"original-job-input"}`)}}}, nil
}

func TestRawJobDataComesFromJobInputNotClientPayload(t *testing.T) {
	engine := &rawInputEngine{}
	svc, err := New(Config{Engine: engine})
	require.NoError(t, err)
	req := model.GetWorkflowRequest{ProjectID: "tenant", WorkflowID: "job"}
	detail, err := svc.GetWorkflow(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, detail.RawJobData)
	require.False(t, engine.requestedInput)
	req.IncludeRawJobData = true
	detail, err = svc.GetWorkflow(context.Background(), req)
	require.NoError(t, err)
	require.True(t, engine.requestedInput)
	require.NotNil(t, detail.RawJobData)
	require.Equal(t, "original-job-input", (*detail.RawJobData)["recipe"])
	require.NotContains(t, *detail.RawJobData, "cursor")
}
