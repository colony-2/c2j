package compiler

import (
	"encoding/json"
	"fmt"
	"time"

	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// TimeoutCheckpointTaskType records scope entry and task admission times in
// the ordinary replay log. It performs no application work.
const TimeoutCheckpointTaskType = coretask.TimeoutCheckpointTaskType

type timeoutCheckpointInput struct {
	Kind    string        `json:"kind"`
	Label   string        `json:"label"`
	Timeout time.Duration `json:"timeout"`
}

type timeoutCheckpointOutput struct {
	At time.Time `json:"at"`
}

type timeoutCheckpointWorker struct{}

// NewTimeoutCheckpointTaskWorker supports hosts that register task workers
// individually rather than using NewRecipeWorkerWithOptions.
func NewTimeoutCheckpointTaskWorker() jobworkflow.TaskWorker { return timeoutCheckpointWorker{} }

func (timeoutCheckpointWorker) Name() string { return TimeoutCheckpointTaskType }
func (timeoutCheckpointWorker) Run(_ jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	if input == nil {
		return nil, fmt.Errorf("missing timeout checkpoint input")
	}
	raw, err := input.GetData()
	if err != nil {
		return nil, err
	}
	var req timeoutCheckpointInput
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if (req.Kind != "scope" && req.Kind != "task") || req.Timeout <= 0 {
		return nil, fmt.Errorf("invalid timeout checkpoint")
	}
	return jobdb.NewTaskData(timeoutCheckpointOutput{At: time.Now().UTC()})
}

func timeoutCheckpoint(ctx jobworkflow.JobContext, kind, label string, timeout time.Duration) (time.Time, error) {
	input, err := jobdb.NewTaskData(timeoutCheckpointInput{Kind: kind, Label: label, Timeout: timeout})
	if err != nil {
		return time.Time{}, err
	}
	output, err := ctx.DoTask(jobdb.RunPolicy{}, TimeoutCheckpointTaskType, input)
	if err != nil {
		return time.Time{}, err
	}
	if output == nil {
		return time.Time{}, fmt.Errorf("missing timeout checkpoint output")
	}
	raw, err := output.GetData()
	if err != nil {
		return time.Time{}, err
	}
	var result timeoutCheckpointOutput
	if err := json.Unmarshal(raw, &result); err != nil {
		return time.Time{}, err
	}
	if result.At.IsZero() {
		return time.Time{}, fmt.Errorf("missing timeout checkpoint time")
	}
	return result.At, nil
}

func withDurableExecutionTimeout(ctx jobworkflow.JobContext, timeout time.Duration, label string) (jobworkflow.JobContext, error) {
	if timeout <= 0 || ctx == nil {
		return ctx, nil
	}
	at, err := timeoutCheckpoint(ctx, "scope", label, timeout)
	if err != nil {
		return nil, fmt.Errorf("record %s timeout scope: %w", label, err)
	}
	wrapped := withExecutionTimeout(ctx, timeout, label).(*timeoutJobContext)
	wrapped.deadline = at.Add(timeout)
	wrapped.durable = true
	return wrapped, nil
}
