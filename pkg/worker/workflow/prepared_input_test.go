package workflow

import (
	"context"
	"errors"
	"testing"

	coretask "github.com/colony-2/c2j/pkg/task"
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

func preparedDataTestEngine() *preparedDataEngine {
	stamp := []byte(`{"at":"2026-10-04T01:02:03Z"}`)
	form := []byte(`{"v":1,"kind":"activity_output","payload":{"output":{"question":"Continue?"},"git":{"persist_hash":"snapshot"},"workspace_scope_id":"scope"}}`)
	return &preparedDataEngine{
		handle: &preparedDataHandle{data: &jobdb.SimpleTaskData{Data: stamp}},
		run: jobdb.GetJobRunResponse{
			Job: jobdb.JobRunSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}},
			Attempts: []jobdb.JobAttempt{{Tasks: []jobdb.TaskRun{
				{TaskType: "input:generate_form", Attempts: []jobdb.TaskAttempt{{Ordinal: 4, Outcome: jobdb.TaskOutcome{Status: jobdb.TaskOutcomeStatusSucceeded}, Output: &jobdb.TaskIO{Data: form}}}},
				{TaskType: coretask.TimeoutCheckpointTaskType, Attempts: []jobdb.TaskAttempt{{Ordinal: 5, InputRef: &jobdb.InputReference{Ordinal: 4}, Input: &jobdb.TaskIO{Data: []byte(`{"kind":"task","label":"op input","timeout":3000000000}`)}, Outcome: jobdb.TaskOutcome{Status: jobdb.TaskOutcomeStatusSucceeded}, Output: &jobdb.TaskIO{Data: stamp}}}},
			}}},
		},
	}
}

func TestPreparedInputFollowsExactAdmissionReference(t *testing.T) {
	e := preparedDataTestEngine()
	control := &SWFWorkflowControl{Engine: e}
	h, err := control.GetWaitingTask(context.Background(), e.handle.JobKey())
	require.NoError(t, err)
	d, err := h.Data()
	require.NoError(t, err)
	raw, err := d.GetData()
	require.NoError(t, err)
	require.Equal(t, e.run.Attempts[0].Tasks[0].Attempts[0].Output.Data, raw, "return the complete original envelope, not just its form")
	require.Equal(t, int64(6), h.TaskOrdinalToComplete())
	// The adapter must not replace completion with a new ordinal/hash contract.
	e.handle.finishErr = jobdb.ErrConflict
	reply := jobdb.NewTaskDataOrPanic(map[string]any{"response": "yes"})
	require.ErrorIs(t, h.Finish(context.Background(), reply), jobdb.ErrConflict)
	require.Same(t, reply, e.handle.finished)
}

func TestPreparedInputRejectsUnrelatedOrCorruptHistory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*preparedDataEngine)
	}{
		{"wrong job", func(e *preparedDataEngine) { e.run.Job.JobKey.JobId = "other" }},
		{"missing checkpoint", func(e *preparedDataEngine) { e.run.Attempts[0].Tasks = e.run.Attempts[0].Tasks[:1] }},
		{"wrong control type", func(e *preparedDataEngine) { e.run.Attempts[0].Tasks[1].TaskType = "another_task" }},
		{"failed checkpoint", func(e *preparedDataEngine) {
			e.run.Attempts[0].Tasks[1].Attempts[0].Outcome.Status = jobdb.TaskOutcomeStatusFailed
		}},
		{"changed checkpoint output", func(e *preparedDataEngine) {
			e.run.Attempts[0].Tasks[1].Attempts[0].Output.Data = []byte(`{"at":"2026-10-05T01:02:03Z"}`)
		}},
		{"missing checkpoint input", func(e *preparedDataEngine) { e.run.Attempts[0].Tasks[1].Attempts[0].Input = nil }},
		{"scope checkpoint", func(e *preparedDataEngine) {
			e.run.Attempts[0].Tasks[1].Attempts[0].Input.Data = []byte(`{"kind":"scope","timeout":1}`)
		}},
		{"missing reference", func(e *preparedDataEngine) { e.run.Attempts[0].Tasks[1].Attempts[0].InputRef = nil }},
		{"reference skips history", func(e *preparedDataEngine) { e.run.Attempts[0].Tasks[1].Attempts[0].InputRef.Ordinal = 3 }},
		{"reference cycle", func(e *preparedDataEngine) { e.run.Attempts[0].Tasks[1].Attempts[0].InputRef.Ordinal = 5 }},
		{"missing producer", func(e *preparedDataEngine) { e.run.Attempts[0].Tasks = e.run.Attempts[0].Tasks[1:] }},
		{"unrelated producer", func(e *preparedDataEngine) { e.run.Attempts[0].Tasks[0].TaskType = "another:prepare" }},
		{"failed producer", func(e *preparedDataEngine) {
			e.run.Attempts[0].Tasks[0].Attempts[0].Outcome.Status = jobdb.TaskOutcomeStatusFailed
		}},
		{"invalid envelope", func(e *preparedDataEngine) {
			e.run.Attempts[0].Tasks[0].Attempts[0].Output.Data = []byte(`{"question":"not an envelope"}`)
		}},
		{"duplicate ordinal", func(e *preparedDataEngine) {
			e.run.Attempts[0].Tasks = append(e.run.Attempts[0].Tasks, e.run.Attempts[0].Tasks[0])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := preparedDataTestEngine()
			tc.change(e)
			h, err := (&SWFWorkflowControl{Engine: e}).GetWaitingTask(context.Background(), e.handle.JobKey())
			require.NoError(t, err)
			_, err = h.Data()
			require.Error(t, err)
		})
	}
}

func TestPreparedInputPreservesStorageErrorsAndUntimedData(t *testing.T) {
	e := preparedDataTestEngine()
	e.err = errors.New("storage unavailable")
	h, err := (&SWFWorkflowControl{Engine: e}).GetWaitingTask(context.Background(), e.handle.JobKey())
	require.NoError(t, err)
	_, err = h.Data()
	require.ErrorIs(t, err, e.err)
	e.handle.data = &jobdb.SimpleTaskData{Data: e.run.Attempts[0].Tasks[0].Attempts[0].Output.Data}
	data, err := h.Data()
	require.NoError(t, err)
	require.Same(t, e.handle.data, data, "untimed form must not depend on reading history")
}
