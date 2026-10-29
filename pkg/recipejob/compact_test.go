package recipejob_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestCompactRecipePageDoesNotEnrichRows(t *testing.T) {
	deadline := time.Now().UTC().Add(time.Hour)
	row := jobdb.JobSummary{
		JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, JobType: "recipe", GoalID: "goal",
		Status: jobdb.JobStatusPendingJobs, CompletionStatus: "future_status", SchemaHash: "schema",
		Metadata:    json.RawMessage(`{"recipe":"fixture","title":"Title","custom":{"n":9007199254740993}}`),
		ParentJobID: "parent", ClientPayloadRevision: 42,
		BlockingWaitSummary: &jobdb.DependencyWaitSummary{WaitID: "wait", Deadline: &deadline},
		// Simulate an older server still populating deprecated projections.
		OtherGoalIDs: []string{"other"}, CompletionDetail: "large detail", WaitFor: []string{"dependency"},
		ClientPayload: json.RawMessage(`{"large":"payload"}`), BlockingWait: &jobdb.DependencyWaitInfo{WaitID: "wait"},
		ExecutionState: jobdb.ExecutionState{TaskWait: &jobdb.TaskWait{OutputOrdinal: 9}},
	}
	rows := make([]jobdb.JobSummary, 200)
	for i := range rows {
		rows[i] = row
	}
	// All other runtime methods panic. Page size must not increase the call count.
	rt := &completionListOnly{list: func(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
		require.Equal(t, 200, req.PageSize)
		require.Equal(t, "before", req.PageToken)
		return jobdb.ListJobsResponse{Jobs: rows, NextPageToken: "after"}, nil
	}}
	page, err := recipejob.ListRecipeJobs(context.Background(), rt, recipejob.ListRecipeJobsRequest{TenantID: "tenant", PageSize: 200, PageToken: "before"})
	require.NoError(t, err)
	require.EqualValues(t, 1, rt.calls.Load())
	require.Equal(t, "after", page.NextPageToken)
	require.Len(t, page.Jobs, 200)
	for _, got := range page.Jobs {
		require.Equal(t, row.BlockingWaitSummary, got.BlockingWaitSummary)
		require.JSONEq(t, string(row.Metadata), string(got.Metadata))
		require.Equal(t, "schema", got.SchemaHash)
		require.Equal(t, "parent", got.Parent.JobID)
		require.Equal(t, "Title", got.Title)
		require.Equal(t, "future_status", got.CompletionStatus)
		require.EqualValues(t, 42, got.ClientPayloadRevision)
		require.Nil(t, got.BlockingWait)
		require.Empty(t, got.OtherGoalIDs)
		require.Empty(t, got.CompletionDetail)
		require.Empty(t, got.WaitFor)
		require.Empty(t, got.ClientPayload)
		require.Nil(t, got.TaskWait)
		require.Equal(t, "not_loaded", got.Execution.Status)
	}
	*page.Jobs[0].BlockingWaitSummary.Deadline = time.Time{}
	require.False(t, row.BlockingWaitSummary.Deadline.IsZero())
	require.Equal(t, "large detail", rows[0].CompletionDetail, "projection must not mutate source rows")
}

type selectedJobReader struct {
	row         jobdb.JobSummary
	info        jobdb.JobInfo
	err         error
	lists, gets int
}

func (r *selectedJobReader) ListJobs(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	r.lists++
	return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{r.row}}, nil
}

func (r *selectedJobReader) GetJob(_ context.Context, key jobdb.JobKey) (jobdb.JobInfo, error) {
	r.gets++
	if key != r.row.JobKey {
		return jobdb.JobInfo{}, errors.New("wrong selected job")
	}
	return r.info, r.err
}

func TestSelectedRecipeJobLoadsDetailsExplicitly(t *testing.T) {
	memory := "8Gi"
	demand, err := execution.Initial(nil, true, execution.Requirements{Resources: execution.Resources{Memory: &memory}})
	require.NoError(t, err)
	payload, err := execution.PayloadWithDemand(nil, demand.Demand)
	require.NoError(t, err)
	r := &selectedJobReader{
		row: jobdb.JobSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, JobType: "recipe", Metadata: json.RawMessage(`{"recipe":"fixture"}`)},
		info: jobdb.JobInfo{Status: jobdb.JobStatusPendingJobs, GoalID: "goal", OtherGoalIDs: []string{"other"},
			ClientPayload: payload, ClientPayloadRevision: 7, CompletionStatus: "future_status", CompletionDetail: "exact detail",
			WaitFor: []string{"dependency"}, BlockingWait: &jobdb.DependencyWaitInfo{WaitID: "wait"},
			NextRoute:      &jobdb.Route{JobType: "recipe", TaskType: "input:collect"},
			ExecutionState: jobdb.ExecutionState{TaskWait: &jobdb.TaskWait{InputOrdinal: 4, OutputOrdinal: 5, InputHash: "hash"}},
		},
	}
	got, err := recipejob.GetRecipeJob(context.Background(), r, recipejob.GetRecipeJobRequest{TenantID: "tenant", JobID: "job"})
	require.NoError(t, err)
	require.Equal(t, 1, r.lists)
	require.Equal(t, 1, r.gets)
	require.Equal(t, r.info.Status, got.Status)
	require.Equal(t, r.info.OtherGoalIDs, got.OtherGoalIDs)
	require.Equal(t, r.info.BlockingWait, got.BlockingWait)
	require.Equal(t, r.info.WaitFor, got.WaitFor)
	require.Equal(t, r.info.NextRoute, got.NextRoute)
	require.Equal(t, r.info.ExecutionState.TaskWait, got.TaskWait)
	require.Equal(t, "exact detail", got.CompletionDetail)
	require.Equal(t, "future_status", got.CompletionStatus)
	require.Equal(t, int64(7), got.ClientPayloadRevision)
	require.JSONEq(t, string(payload), string(got.ClientPayload))
	require.Equal(t, "yield", got.Execution.Source)
	require.Equal(t, memory, *got.Execution.Demand.Effective.Resources.Memory)
	r.err = jobdb.ErrJobNotFound
	_, err = recipejob.GetRecipeJob(context.Background(), r, recipejob.GetRecipeJobRequest{TenantID: "tenant", JobID: "job"})
	require.ErrorIs(t, err, recipejob.ErrJobNotFound)
	_, err = joblist.ListExecutionJobs(context.Background(), r, jobdb.ListJobsRequest{}, &execution.Filter{})
	require.ErrorIs(t, err, joblist.ErrExecutionDetailsRequired)
	require.Equal(t, 2, r.lists)
	require.Equal(t, 2, r.gets, "filter rejection must not fetch any job details")
}
