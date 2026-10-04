package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// preparedInputTaskHandle adapts only Data. Finish still uses the original
// JobDB handle, including its admission ordinal, hash, and completion guards.
// Nothing in durable history or deadline calculation is rewritten.
type preparedInputTaskHandle struct {
	jobworkflow.TaskHandle
	engine jobworkflow.Engine
	ctx    context.Context
}

func (h *preparedInputTaskHandle) Data() (jobdb.TaskData, error) {
	data, err := h.TaskHandle.Data()
	if err != nil {
		return nil, err
	}
	raw, err := data.GetData()
	if err != nil {
		return nil, err
	}
	// Ordinary, untimed handoffs already point to the form. Only consider
	// unwrapping a timestamp, then verify its durable control-task identity.
	var stamp struct {
		At time.Time `json:"at"`
	}
	if json.Unmarshal(raw, &stamp) != nil || stamp.At.IsZero() {
		return data, nil
	}
	run, err := h.engine.GetJobRun(h.ctx, jobdb.GetJobRunRequest{
		JobKey: h.JobKey(), IncludeInputs: true, IncludeOutputs: true, IncludeArtifacts: true,
	})
	if err != nil {
		return nil, err
	}
	if run.Job.JobKey != h.JobKey() {
		return nil, fmt.Errorf("prepared input job identity does not match")
	}
	ordinal := h.TaskOrdinalToComplete() - 1
	kind, checkpoint, err := taskAttemptAt(run, ordinal)
	if err != nil {
		return nil, err
	}
	if kind != coretask.TimeoutCheckpointTaskType || !successfulTaskOutput(checkpoint) ||
		!bytes.Equal(checkpoint.Output.Data, raw) || checkpoint.Input == nil || checkpoint.InputRef == nil {
		return nil, fmt.Errorf("invalid input admission checkpoint at ordinal %d", ordinal)
	}
	var admission struct {
		Kind    string        `json:"kind"`
		Timeout time.Duration `json:"timeout"`
	}
	if err := json.Unmarshal(checkpoint.Input.Data, &admission); err != nil || admission.Kind != "task" || admission.Timeout <= 0 {
		return nil, fmt.Errorf("invalid input admission checkpoint input at ordinal %d", ordinal)
	}
	// A task admission checkpoint immediately follows the preceding application
	// result. Follow that recorded reference exactly; never search for an older
	// form when this association is missing or points to another kind of task.
	producerOrdinal := checkpoint.InputRef.Ordinal
	if producerOrdinal < 0 || producerOrdinal != ordinal-1 {
		return nil, fmt.Errorf("invalid prepared input reference at ordinal %d", ordinal)
	}
	kind, producer, err := taskAttemptAt(run, producerOrdinal)
	if err != nil {
		return nil, err
	}
	if kind != "input:generate_form" || !successfulTaskOutput(producer) {
		return nil, fmt.Errorf("input admission does not reference a successful form preparation at ordinal %d", producerOrdinal)
	}
	var envelope coretask.OutputEnvelope
	if err := json.Unmarshal(producer.Output.Data, &envelope); err != nil ||
		envelope.Version != coretask.OutputEnvelopeVersion || envelope.Kind != coretask.OutputKindActivityInvocationOutput {
		return nil, fmt.Errorf("invalid prepared input envelope at ordinal %d", producerOrdinal)
	}
	return taskIOToJobData(producer.Output, h.engine, h.JobKey().TenantId, h.JobKey(), producerOrdinal)
}

func successfulTaskOutput(attempt *jobdb.TaskAttempt) bool {
	return attempt != nil && attempt.Outcome.Status == jobdb.TaskOutcomeStatusSucceeded && attempt.Outcome.Error == nil && attempt.Output != nil
}

func taskAttemptAt(run jobdb.GetJobRunResponse, ordinal int64) (string, *jobdb.TaskAttempt, error) {
	var found *jobdb.TaskAttempt
	var kind string
	for _, job := range run.Attempts {
		for _, task := range job.Tasks {
			for _, attempt := range task.Attempts {
				if attempt.Ordinal == ordinal {
					if found != nil {
						return "", nil, fmt.Errorf("duplicate task ordinal %d", ordinal)
					}
					copy := attempt
					found, kind = &copy, task.TaskType
				}
			}
		}
	}
	if found == nil {
		return "", nil, fmt.Errorf("task ordinal %d not found for prepared input", ordinal)
	}
	return kind, found, nil
}
