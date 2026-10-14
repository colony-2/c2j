package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type preparedDataEngine struct {
	jobworkflow.Engine
	run    jobdb.GetJobRunResponse
	err    error
	handle *preparedDataHandle
}

func (e *preparedDataEngine) GetWaitingTask(context.Context, jobdb.JobKey) (jobworkflow.TaskHandle, error) {
	return e.handle, nil
}
func (e *preparedDataEngine) GetJobRun(context.Context, jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error) {
	return e.run, e.err
}

type preparedDataHandle struct {
	jobworkflow.TaskHandle
	data      jobdb.TaskData
	finished  jobdb.TaskData
	finishErr error
}

func (h *preparedDataHandle) JobKey() jobdb.JobKey {
	return jobdb.JobKey{TenantId: "tenant", JobId: "job"}
}
func (h *preparedDataHandle) TaskType() string              { return "input:collect_user_input" }
func (h *preparedDataHandle) TaskOrdinalToComplete() int64  { return 6 }
func (h *preparedDataHandle) Data() (jobdb.TaskData, error) { return h.data, nil }
func (h *preparedDataHandle) Finish(_ context.Context, d jobdb.TaskData) error {
	h.finished = d
	return h.finishErr
}

func TestWaitingInputUsesOwnSnapshot(t *testing.T) {
	e := &preparedDataEngine{handle: &preparedDataHandle{data: jobdb.NewTaskDataOrPanic(map[string]any{"input": map[string]any{"question": "Continue?"}})}, err: errors.New("history must not be read")}
	h, err := (&SWFWorkflowControl{Engine: e}).GetWaitingTask(context.Background(), e.handle.JobKey())
	require.NoError(t, err)
	data, err := h.Data()
	require.NoError(t, err)
	require.Same(t, e.handle.data, data)
	require.Equal(t, int64(6), h.TaskOrdinalToComplete())
	e.handle.finishErr = jobdb.ErrConflict
	reply := jobdb.NewTaskDataOrPanic(map[string]any{"response": "yes"})
	require.ErrorIs(t, h.Finish(context.Background(), reply), jobdb.ErrConflict)
	require.Same(t, reply, e.handle.finished)
}
