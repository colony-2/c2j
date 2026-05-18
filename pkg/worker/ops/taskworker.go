package ops

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/swf-go/pkg/swf"
)

type taskWorker struct {
	name string
	reg  ActivityRegistration
	doer *opExecutor
}

func (t *taskWorker) Name() string {
	return t.name
}

func (t *taskWorker) Run(ctx swf.TaskContext, input swf.TaskData) (swf.TaskData, error) {
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
	return swf.NewTaskData(env, outArt...)
}

var _ swf.TaskWorker = &taskWorker{}

const (
	taskDeadlinePollInterval = 250 * time.Millisecond
	taskDeadlineCancelDelay  = 100 * time.Millisecond
)

// NewTaskExecutionContext returns a context that is canceled when the SWF task context reports
// its durable deadline has elapsed. The deadline source remains SWF chapter metadata, not c2j
// wall-clock bookkeeping, so replay keeps the original task/job start time semantics.
func NewTaskExecutionContext(taskCtx swf.TaskContext) (context.Context, context.CancelFunc) {
	ctx, cancelCause := context.WithCancelCause(context.Background())

	go monitorTaskDeadline(ctx, cancelCause, taskCtx)

	return ctx, func() {
		cancelCause(context.Canceled)
	}
}

func monitorTaskDeadline(ctx context.Context, cancelCause context.CancelCauseFunc, taskCtx swf.TaskContext) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := taskCtx.AwaitDuration(swf.Duration(taskDeadlinePollInterval))
		if err == nil {
			continue
		}
		var cacheMiss swf.ReplayCacheMissError
		if errors.As(err, &cacheMiss) {
			return
		}

		cause := err
		var timeoutErr *swf.TimeoutError
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

func failedTaskData(output ActivityInvocationOutput, artifacts []swf.Artifact) (swf.TaskData, error) {
	if !hasFailedActivityPayload(output, artifacts) {
		return nil, nil
	}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, output)
	if err != nil {
		return nil, err
	}
	return swf.NewTaskData(env, artifacts...)
}

func hasFailedActivityPayload(output ActivityInvocationOutput, artifacts []swf.Artifact) bool {
	if len(artifacts) > 0 {
		return true
	}
	if len(output.OpOutput) > 0 || output.NextTask != "" || len(output.ArtifactRefs) > 0 {
		return true
	}
	return output.GitResult != (contextual.GitCommitContext{})
}
