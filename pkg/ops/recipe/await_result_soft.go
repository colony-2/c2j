package recipe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const (
	returnWhenTerminal      = "terminal"
	returnWhenCurrentStatus = "current_status"
)

type AwaitResultSoftInput struct {
	Target       WaitTarget `json:"target"`
	Wait         WaitPolicy `json:"wait,omitempty"`
	ReturnWhen   string     `json:"return_when,omitempty"`
	Timeout      string     `json:"timeout,omitempty"`
	PollInterval string     `json:"poll_interval,omitempty"`
}

type ChildStatusOutput struct {
	GoalID                    string                         `json:"goal_id,omitempty"`
	GoalState                 jobdb.GoalState                `json:"goal_state,omitempty"`
	Result                    ResultSource                   `json:"result"`
	GoalFailure               *jobdb.GoalFailure             `json:"goal_failure,omitempty"`
	JobID                     string                         `json:"job_id"`
	Terminal                  bool                           `json:"terminal"`
	Status                    string                         `json:"status"`
	FailureKind               string                         `json:"failure_kind,omitempty"`
	FailureMessage            string                         `json:"failure_message,omitempty"`
	Outputs                   map[string]interface{}         `json:"outputs,omitempty"`
	Artifacts                 map[string]recipeartifacts.Ref `json:"artifacts,omitempty"`
	PartialOutputsAvailable   bool                           `json:"partial_outputs_available"`
	PartialArtifactsAvailable bool                           `json:"partial_artifacts_available"`
	StartedAt                 string                         `json:"started_at,omitempty"`
	FinishedAt                string                         `json:"finished_at,omitempty"`
}

func awaitResultSoft(deps ops.OpDependencies, ctx context.Context, input AwaitResultSoftInput) (ChildStatusOutput, error) {
	if deps == nil || deps.JobTool() == nil || deps.WorkflowControl() == nil {
		return ChildStatusOutput{}, fmt.Errorf("wait requires job tool and workflow control")
	}
	if (input.Target.Job == "") == (input.Target.Goal == "") {
		return ChildStatusOutput{}, fmt.Errorf("target requires exactly one job or goal")
	}
	if input.Wait.On == "job" && input.Target.Goal != "" {
		return ChildStatusOutput{}, fmt.Errorf("exact job wait requires target.job")
	}
	if input.Wait.On == "job" && input.Wait.Condition != "" && input.Wait.Condition != "complete" {
		return ChildStatusOutput{}, fmt.Errorf("soft job waits require complete")
	}
	if input.Wait.On != "job" {
		return awaitGoalSoft(deps, ctx, input)
	}
	jobID := strings.TrimSpace(input.Target.Job)
	if jobID == "" {
		return ChildStatusOutput{}, fmt.Errorf("job_id is required")
	}
	returnWhen := strings.TrimSpace(input.ReturnWhen)
	if returnWhen == "" {
		returnWhen = returnWhenTerminal
	}
	switch returnWhen {
	case returnWhenTerminal, returnWhenCurrentStatus:
	default:
		return ChildStatusOutput{}, fmt.Errorf("unsupported return_when %q", input.ReturnWhen)
	}
	if strings.TrimSpace(input.Timeout) != "" || strings.TrimSpace(input.PollInterval) != "" {
		return ChildStatusOutput{}, fmt.Errorf("timeout and poll_interval are not supported for recipe.await_result_soft v1")
	}
	if deps == nil || deps.JobTool() == nil {
		return ChildStatusOutput{}, fmt.Errorf("recipe.await_result_soft requires job tool dependency")
	}
	if deps.WorkflowControl() == nil {
		return ChildStatusOutput{}, fmt.Errorf("recipe.await_result_soft requires workflow control dependency")
	}

	if returnWhen == returnWhenTerminal {
		if err := awaitExactTerminal(deps, ctx, jobID); err != nil && !isSoftChildTerminalError(err) {
			return ChildStatusOutput{}, err
		}
	}

	key := jobdb.JobKey{TenantId: deps.JobTool().GetJobKey().TenantId, JobId: jobID}
	if inspector, ok := deps.WorkflowControl().(workflowctl.JobInspector); ok {
		inspection, err := inspector.InspectJob(ctx, key)
		if err != nil {
			return ChildStatusOutput{}, err
		}
		return childStatusFromInspection(deps, inspection)
	}

	if returnWhen == returnWhenCurrentStatus {
		return ChildStatusOutput{}, fmt.Errorf("workflow control does not support job inspection for return_when=current_status")
	}
	return childStatusFromJobResultFallback(deps, ctx, key)
}

func isSoftChildTerminalError(err error) bool {
	return errors.Is(err, jobdb.ErrJobFailed) || errors.Is(err, jobdb.ErrJobCancelled)
}

func childStatusFromInspection(deps ops.OpDependencies, inspection workflowctl.JobInspection) (ChildStatusOutput, error) {
	out := ChildStatusOutput{
		JobID:       inspection.JobKey.JobId,
		GoalID:      inspection.GoalID,
		Result:      ResultSource{Job: inspection.JobKey.JobId},
		Terminal:    inspection.Terminal,
		Status:      normalizeChildStatus(inspection.Status, inspection.Terminal),
		FailureKind: strings.TrimSpace(inspection.FailureKind),
	}
	if out.FailureKind == "" && out.Status == "completed" {
		out.FailureKind = "none"
	}
	out.FailureMessage = strings.TrimSpace(inspection.FailureMessage)
	out.StartedAt = formatInspectionTime(inspection.StartedAt)
	out.FinishedAt = formatInspectionTime(inspection.FinishedAt)

	if inspection.Output == nil {
		return out, nil
	}
	decoded, err := decodeRecipeJobOutput(deps, inspection.Output)
	if err != nil {
		return ChildStatusOutput{}, err
	}
	out.Outputs = decoded.Outputs
	out.Artifacts = decoded.Artifacts
	if out.Status != "completed" {
		out.PartialOutputsAvailable = decoded.OutputAvailable
		out.PartialArtifactsAvailable = decoded.ArtifactsAvailable
	}
	return out, nil
}

func childStatusFromJobResultFallback(deps ops.OpDependencies, ctx context.Context, key jobdb.JobKey) (ChildStatusOutput, error) {
	data, err := deps.WorkflowControl().JobResult(ctx, key)
	if err != nil {
		if errors.Is(err, jobdb.ErrJobFailed) || errors.Is(err, jobdb.ErrJobCancelled) {
			return ChildStatusOutput{
				JobID:          key.JobId,
				Terminal:       true,
				Status:         fallbackStatusFromJobResultError(err),
				FailureKind:    fallbackFailureKind(err),
				FailureMessage: err.Error(),
			}, nil
		}
		return ChildStatusOutput{}, err
	}
	decoded, err := decodeRecipeJobOutput(deps, data)
	if err != nil {
		return ChildStatusOutput{}, err
	}
	return ChildStatusOutput{
		JobID:       key.JobId,
		Result:      ResultSource{Job: key.JobId},
		Terminal:    true,
		Status:      "completed",
		FailureKind: "none",
		Outputs:     decoded.Outputs,
		Artifacts:   decoded.Artifacts,
	}, nil
}

func normalizeChildStatus(status string, terminal bool) string {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case "running", "completed", "failed", "cancelled", "timed_out", "unknown":
		return status
	case "":
		if terminal {
			return "unknown"
		}
		return "running"
	default:
		return "unknown"
	}
}

func fallbackStatusFromJobResultError(err error) string {
	if errors.Is(err, jobdb.ErrJobCancelled) {
		return "cancelled"
	}
	return "failed"
}

func fallbackFailureKind(err error) string {
	if errors.Is(err, jobdb.ErrJobCancelled) {
		return "cancellation"
	}
	var timeoutErr *jobdb.TimeoutError
	if errors.As(err, &timeoutErr) {
		return "timeout"
	}
	if jobdb.IsSystemError(err) {
		return "system_error"
	}
	return "task_error"
}

func formatInspectionTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func awaitExactTerminal(deps ops.OpDependencies, ctx context.Context, id string) error {
	_, err := awaitChild(deps, ctx, StartedJob{JobID: id, Wait: WaitPolicy{On: "job", Condition: "complete"}}, false)
	return err
}
func awaitGoalSoft(deps ops.OpDependencies, ctx context.Context, in AwaitResultSoftInput) (ChildStatusOutput, error) {
	if in.Wait.On != "" && in.Wait.On != "goal" {
		return ChildStatusOutput{}, fmt.Errorf("wait.on must be goal or job")
	}
	if in.Wait.Condition != "" && in.Wait.Condition != "success" {
		return ChildStatusOutput{}, fmt.Errorf("goal waits require success")
	}
	if in.Timeout != "" || in.PollInterval != "" {
		return ChildStatusOutput{}, fmt.Errorf("use the enclosing node timeout")
	}
	if in.Target.Job != "" && in.Target.Goal != "" {
		return ChildStatusOutput{}, fmt.Errorf("target requires exactly one job or goal")
	}
	id := in.Target.Goal
	job := in.Target.Job
	if id == "" && job != "" {
		inspector, ok := deps.WorkflowControl().(workflowctl.JobInspector)
		if !ok {
			return ChildStatusOutput{}, fmt.Errorf("job inspection unavailable")
		}
		info, err := inspector.InspectJob(ctx, jobdb.JobKey{TenantId: deps.JobTool().GetJobKey().TenantId, JobId: job})
		if err != nil {
			return ChildStatusOutput{}, err
		}
		id = info.GoalID
	}
	if id == "" {
		return ChildStatusOutput{}, fmt.Errorf("goal target is required")
	}
	out := ChildStatusOutput{JobID: job, GoalID: id}
	if in.ReturnWhen == returnWhenCurrentStatus {
		reader, ok := deps.WorkflowControl().(goalInspector)
		if !ok {
			return out, fmt.Errorf("goal inspection unavailable")
		}
		goal, err := reader.GetGoal(ctx, jobdb.GoalKey{TenantId: deps.JobTool().GetJobKey().TenantId, GoalID: id})
		if err != nil {
			return out, err
		}
		out.GoalState = goal.State
		out.Terminal = goal.State != jobdb.GoalOpen
		out.Status = strings.ToLower(string(goal.State))
		out.GoalFailure = goal.Failure
		if goal.Failure != nil {
			out.FailureMessage = goal.Failure.Message
			out.FailureKind = "goal_failed"
		}
		if goal.State == jobdb.GoalOpen || goal.State == jobdb.GoalFailed {
			return out, nil
		}
		out.Result = ResultSource{Goal: id, Job: goal.SuccessfulJobID}
	} else {
		if in.ReturnWhen != "" && in.ReturnWhen != returnWhenTerminal {
			return out, fmt.Errorf("unsupported return_when %q", in.ReturnWhen)
		}
		binding, err := awaitChild(deps, ctx, StartedJob{GoalID: id}, true)
		if err != nil {
			var failed *jobdb.GoalFailedError
			if errors.As(err, &failed) {
				out.Terminal = true
				out.Status = "failed"
				out.GoalState = jobdb.GoalFailed
				out.GoalFailure = &failed.Failure
				out.FailureMessage = failed.Failure.Message
				out.FailureKind = "goal_failed"
				return out, nil
			}
			return out, err
		}
		out.Result = ResultSource{Goal: id, Job: binding.SatisfiedByJobID}
	}
	result, err := getRecipeOutput(deps, ctx, StartedJob{JobID: out.Result.Job})
	if err != nil {
		return out, err
	}
	out.GoalState = jobdb.GoalSucceeded
	out.Terminal = true
	out.Status = "completed"
	out.Outputs = result.Outputs
	out.Artifacts = result.Artifacts
	return out, nil
}
