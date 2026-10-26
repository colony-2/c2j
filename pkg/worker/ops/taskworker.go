package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/secrets"
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

func (t *taskWorker) Run(tc jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	return t.RunTask(tc.Context(), tc, input)
}

func (t *taskWorker) RunTask(local context.Context, ctx jobworkflow.TaskContext, input jobdb.TaskData) (result jobdb.TaskData, runErr error) {
	redactor := secrets.RedactorFromContext(local)
	if redactor == nil {
		redactor = secrets.NewRedactor()
		defer redactor.Close()
		local = secrets.WithRedactor(local, redactor)
	}
	session := redactor.NewSession()
	defer session.Close()

	logger := secrets.MaskLogger(ctx.Logger, redactor).With("component", "worker", "purpose", "run",
		"tenant_id", ctx.JobKey.TenantId, "job_id", ctx.JobKey.JobId,
		"task_type", t.name, "op_type", t.reg.Metadata.Type, "step", t.reg.Step.Name,
		"chapter_ordinal", ctx.Step, "invocation_id", ctx.InvocationID)
	if ctx.AttemptNumber > 0 {
		logger = logger.With("attempt", ctx.AttemptNumber)
	}
	started := time.Now()
	logger.Debug("Task attempt started", "event", "task.attempt.started")
	defer func() {
		outcome := "succeeded"
		switch {
		case jobworkflow.IsExecutionSuspended(runErr):
			outcome = "waiting"
		case errors.Is(runErr, context.Canceled):
			outcome = "canceled"
		case runErr != nil:
			outcome = "failed"
		}
		logger.Debug("Task attempt finished", "event", "task.attempt.finished", "outcome", outcome,
			"duration_ms", time.Since(started).Milliseconds(), "error", runErr)
	}()
	if err := ctx.Context().Err(); err != nil {
		return nil, err
	}
	d, err := input.GetData()
	if err != nil {
		return nil, err
	}
	air := ActivityInvocationRequest{}
	if err := json.Unmarshal(d, &air); err != nil {
		return nil, err
	}
	logger = logger.With("node_path", air.GitTaskContext.NodePath, "invoke_hash", air.GitTaskContext.InvokeHash)
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
		// Keep preparation evidence attached to the consuming failed outcome.
		runErr = session.Error(runErr)
		if err := session.CheckOutput(diagnostic); err != nil {
			runErr = errors.Join(runErr, err)
			return
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
	opCtx, cancel := context.WithCancel(local)
	defer cancel()
	access := permissions.FromContext(opCtx)
	if access == nil {
		return nil, errors.New("task requires executionruntime.WrapTaskWorker")
	}
	req, err := ResolveInvocation(opCtx, &air)
	resolved = req.Extension
	if err != nil {
		return nil, err
	}
	if req.Extension != nil && req.Extension.Spec.Timeout != "" {
		duration, err := time.ParseDuration(req.Extension.Spec.Timeout)
		if err != nil || duration <= 0 {
			return nil, fmt.Errorf("restricted extension requires a valid positive timeout")
		}
		deadline := time.Now().Add(duration)
		stopAccess := access.LimitDeadline(deadline)
		defer stopAccess()
		var stopOp context.CancelFunc
		opCtx, stopOp = context.WithDeadline(opCtx, deadline)
		defer stopOp()
	}
	configDiag, err = materializeConfiguration(opCtx, t.reg, &air, ctx.GetConfiguration)
	if err != nil {
		return nil, err
	}
	if err := validatePreparedInputs(&air, req); err != nil {
		return nil, err
	}
	if err := secrets.ValidateType(t.reg.Step.InputType, air.Input); err != nil {
		return nil, err
	}
	if t.reg.Metadata.Type == extops.ExecutionOpType && secrets.Contains(air.Input["inputs"]) {
		if req.Extension == nil {
			return nil, errors.New("secret extension inputs require a prepared manifest")
		}

	}
	refs, err := secrets.InvocationReferences(t.reg.Step.InputType, air.Input)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		// Empty-name references carry literal defaults and never query JobDB.
		if ref.Name != "" {
			names = append(names, ref.Name)
		}
	}
	if err := access.CheckSecrets(names); err != nil {
		return nil, err
	}
	if err := InstallInvocation(opCtx, &air, req, PrepareTools); err != nil {
		return nil, err
	}
	secretStart := time.Now()
	if len(refs) > 0 {
		err := session.PrepareReferences(opCtx, refs, access.Resolve)
		air.SecretSetupMS = time.Since(secretStart).Milliseconds()
		if err != nil {
			return nil, session.Error(err)
		}
	}
	opCtx = secrets.WithSession(opCtx, session)
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
	jobTool := &ops.TaskBasedJobTool{TaskContext: ctx, Restricted: access}
	out, outArt, err := t.doer.doLogged(opCtx, jobTool, air, inArt, logger)
	out.PreparationMS = prepMS
	out.SecretSetupMS = air.SecretSetupMS
	out.Configuration = configDiag
	if air.Setup != nil {
		out.Extension = air.Setup.Extension
		air.Setup.Diagnostics.TaskOrdinal = &ctx.Step
		out.Setup = &air.Setup.Diagnostics
	}
	err = session.Error(err)
	if e := session.CheckOutput(out); e != nil {
		return nil, e
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
