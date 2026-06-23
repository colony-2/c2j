package ops

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type taskWorker struct {
	name string
	reg  ActivityRegistration
	doer *opExecutor
}

func (t *taskWorker) Name() string {
	return t.name
}

func (t *taskWorker) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	d, err := input.GetData()
	if err != nil {
		return nil, err
	}
	air := ActivityInvocationRequest{}
	if err := json.Unmarshal(d, &air); err != nil {
		return nil, err
	}
	inArt, err := input.GetArtifacts()
	if err != nil {
		return nil, err
	}
	opCtx, cancel := NewTaskExecutionContext(ctx)
	defer cancel()
	jobTool := &ops.TaskBasedJobTool{TaskContext: ctx}
	out, outArt, err := t.doer.do(opCtx, jobTool, air, inArt)
	if err != nil {
		td, tdErr := failedTaskData(out, outArt)
		if tdErr != nil {
			return nil, errors.Join(err, tdErr)
		}
		if td != nil {
			return td, err
		}
		return nil, err
	}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, out)
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(env, outArt...)
}

var _ jobworkflow.TaskWorker = &taskWorker{}

const (
	taskDeadlinePollInterval = 250 * time.Millisecond
	taskDeadlineCancelDelay  = 100 * time.Millisecond
)

// NewTaskExecutionContext returns a context that is canceled when the SWF task context reports
// its durable deadline has elapsed. The deadline source remains SWF chapter metadata, not c2j
// wall-clock bookkeeping, so replay keeps the original task/job start time semantics.
func NewTaskExecutionContext(taskCtx jobworkflow.TaskContext) (context.Context, context.CancelFunc) {
	ctx, cancelCause := context.WithCancelCause(context.Background())

	go monitorTaskDeadline(ctx, cancelCause, taskCtx)

	return ctx, func() {
		cancelCause(context.Canceled)
	}
}

func monitorTaskDeadline(ctx context.Context, cancelCause context.CancelCauseFunc, taskCtx jobworkflow.TaskContext) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := taskCtx.AwaitDuration(jobdb.Duration(taskDeadlinePollInterval))
		if err == nil {
			continue
		}
		var cacheMiss jobworkflow.ReplayCacheMissError
		if errors.As(err, &cacheMiss) {
			return
		}

		cause := err
		var timeoutErr *jobdb.TimeoutError
		if errors.As(err, &timeoutErr) {
			cause = context.DeadlineExceeded
		}

		timer := time.NewTimer(taskDeadlineCancelDelay)
		select {
		case <-timer.C:
			cancelCause(cause)
		case <-ctx.Done():
		}
		timer.Stop()
		return
	}
}

func failedTaskData(output ActivityInvocationOutput, artifacts []jobdb.Artifact) (jobdb.TaskData, error) {
	if !hasFailedActivityPayload(output, artifacts) {
		return nil, nil
	}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, output)
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(env, artifacts...)
}

func hasFailedActivityPayload(output ActivityInvocationOutput, artifacts []jobdb.Artifact) bool {
	if len(artifacts) > 0 {
		return true
	}
	if len(output.OpOutput) > 0 || output.NextTask != "" || len(output.ArtifactRefs) > 0 {
		return true
	}
	return output.GitResult != (contextual.GitCommitContext{})
}
