package ops

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/secrets"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type taskWorker struct {
	sessions *secrets.Sessions
	name     string
	reg      ActivityRegistration
	doer     *opExecutor
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
	// This worker is called only on a task-result miss. A lost prepared
	// environment records a control result; the compiler returns to setup.
	if !air.Setup.Ready() {
		env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, ActivityInvocationOutput{SetupRequired: true})
		if err != nil {
			return nil, err
		}
		return jobdb.NewTaskData(env)
	}
	if err := secrets.ValidateType(t.reg.Step.InputType, air.Input); err != nil {
		return nil, err
	}
	if t.reg.Metadata.Type == extops.ExecutionOpType && secrets.Contains(air.Input["inputs"]) {
		if air.Setup == nil || air.Setup.Extension == nil {
			return nil, errors.New("secret extension inputs require a prepared manifest")
		}
		raw, _ := air.Input["inputs"].(map[string]any)
		if err := air.Setup.Extension.ValidateInvocationInputs(raw); err != nil {
			return nil, err
		}
	}
	names, err := secrets.InvocationNames(t.reg.Step.InputType, air.Input)
	if err != nil {
		return nil, err
	}
	session := t.sessions.Get(ctx.JobKey)
	if !session.Ready(names) {
		env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, ActivityInvocationOutput{SecretSetupRequired: true})
		if err != nil {
			return nil, err
		}
		return jobdb.NewTaskData(env)
	}
	opCtx, cancel := NewTaskExecutionContext(ctx)
	defer cancel()
	if session != nil {
		opCtx = secrets.WithSession(opCtx, session)
	}
	if air.Setup != nil {
		if air.Setup.Environment != nil {
			opCtx = process.WithToolPath(opCtx, air.Setup.Environment.Path)
		}
		if air.Setup.Extension != nil {
			opCtx = extops.WithPreparedOp(opCtx, air.Setup.Extension)
		}
	}
	jobTool := &ops.TaskBasedJobTool{TaskContext: ctx}
	out, outArt, err := t.doer.do(opCtx, jobTool, air, inArt)
	out.SecretSetupMS = air.SecretSetupMS
	if session != nil {
		err = session.Error(err)
		if e := session.CheckOutput(out); e != nil {
			return nil, e
		}
	}
	if air.Setup != nil {
		out.Setup = &air.Setup.Diagnostics
	}
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
	if output.Setup != nil || output.SetupRequired || output.SecretSetupRequired || output.SecretSetupMS != 0 {
		return true
	}
	if len(artifacts) > 0 {
		return true
	}
	if len(output.OpOutput) > 0 || output.NextTask != "" || len(output.ArtifactRefs) > 0 {
		return true
	}
	if len(output.Jobs.JobIDs) > 0 || len(output.Jobs.Items) > 0 {
		return true
	}
	return output.GitResult != (contextual.GitCommitContext{})
}
