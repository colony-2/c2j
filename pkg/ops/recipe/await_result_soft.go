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
	JobID        string `json:"job_id" validate:"required"`
	ReturnWhen   string `json:"return_when,omitempty"`
	Timeout      string `json:"timeout,omitempty"`
	PollInterval string `json:"poll_interval,omitempty"`
}

type ChildStatusOutput struct {
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
	jobID := strings.TrimSpace(input.JobID)
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
		if err := deps.JobTool().AwaitJobs(jobID); err != nil && !isSoftChildTerminalError(err) {
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
