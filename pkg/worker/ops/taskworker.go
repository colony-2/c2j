package ops

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/colony-2/c2j/pkg/configuration"
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

func (t *taskWorker) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (result jobdb.TaskData, runErr error) {
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
	prepStart := time.Now()
	var configDiag *configuration.Diagnostics
	var resolved *extops.ResolvedOp
	var prepMS int64
	var prepared bool
	defer func() {
		if runErr == nil || (air.Preparation.Extension == nil && len(air.Preparation.Scopes) == 0 && configDiag == nil && !configuration.Contains(air.Input) && !secrets.Contains(air.Input)) {
			return
		}
		if !prepared {
			prepMS = time.Since(prepStart).Milliseconds()
		}
		diagnostic := ActivityInvocationOutput{Configuration: configDiag, Extension: resolved, PreparationMS: prepMS, SecretSetupMS: air.SecretSetupMS}
		if air.Setup != nil {
			air.Setup.Diagnostics.TaskOrdinal = &ctx.Step
			diagnostic.Setup = &air.Setup.Diagnostics
		}
		// JobDB persists error artifacts, but discards error return data.
		// Keep preparation evidence attached to the consuming failed outcome.
		if session := t.sessions.Get(ctx.JobKey); session != nil {
			runErr = session.Error(runErr)
			if err := session.CheckOutput(diagnostic); err != nil {
				runErr = errors.Join(runErr, err)
				return
			}
		}
		raw, err := json.Marshal(diagnostic)
		if err != nil {
			runErr = errors.Join(runErr, err)
			return
		}
		var artifacts []jobdb.Artifact
		var payload json.RawMessage
		if result != nil {
			payload, err = result.GetData()
			if err != nil {
				runErr = errors.Join(runErr, err)
				return
			}
			artifacts, err = result.GetArtifacts()
			if err != nil {
				runErr = errors.Join(runErr, err)
				return
			}
		}
		artifacts = append(artifacts, jobdb.NewArtifactFromBytes("_c2j/preparation.json", raw))
		result, err = jobdb.NewTaskData(payload, artifacts...)
		runErr = errors.Join(runErr, err)
	}()
	opCtx, cancel := NewTaskExecutionContext(ctx)
	defer cancel()
	req, err := ResolveInvocation(opCtx, &air)
	resolved = req.Extension
	if err != nil {
		return nil, err
	}
	configDiag, err = materializeConfiguration(opCtx, t.reg, &air, ctx.GetConfiguration)
	if err != nil {
		return nil, err
	}
	if err := InstallInvocation(opCtx, &air, req, PrepareTools); err != nil {
		return nil, err
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
	secretStart := time.Now()
	if len(names) > 0 {
		if session == nil {
			return nil, errors.New("secret setup requires a local job execution")
		}
		err := session.Prepare(opCtx, names, air.Preparation.SecretCandidates, ctx.ResolveSecrets)
		air.SecretSetupMS = time.Since(secretStart).Milliseconds()
		if err != nil {
			return nil, session.Error(err)
		}
	}
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
	prepMS = time.Since(prepStart).Milliseconds()
	prepared = true
	if err := opCtx.Err(); err != nil {
		return nil, err
	}
	jobTool := &ops.TaskBasedJobTool{TaskContext: ctx}
	out, outArt, err := t.doer.do(opCtx, jobTool, air, inArt)
	out.PreparationMS = prepMS
	out.SecretSetupMS = air.SecretSetupMS
	out.Configuration = configDiag
	if air.Setup != nil {
		out.Extension = air.Setup.Extension
		air.Setup.Diagnostics.TaskOrdinal = &ctx.Step
		out.Setup = &air.Setup.Diagnostics
	}
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
	if output.Configuration != nil || output.Extension != nil || output.Setup != nil || output.SecretSetupMS != 0 {
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
