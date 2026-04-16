package input

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/colony-2/c2j/recipe-core/pkg/ops"
	coretask "github.com/colony-2/c2j/recipe-core/pkg/task"
	"github.com/colony-2/c2j/recipe-core/pkg/workflowctl"
	workerops "github.com/colony-2/c2j/recipe-worker/pkg/ops"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/fatih/structs"
)

var ErrInputNotPending = errors.New("input is not pending for job")

type Runtime struct {
	ctl workflowctl.WorkflowControl
	sse ops.SSEManager
}

func NewRuntime(ctl workflowctl.WorkflowControl, sse ops.SSEManager) (*Runtime, error) {
	if ctl == nil {
		return nil, fmt.Errorf("workflow control must be provided")
	}
	return &Runtime{ctl: ctl, sse: sse}, nil
}

func NewRuntimeFromDeps(deps ops.ServiceDependencies2) (*Runtime, error) {
	if deps == nil {
		return nil, fmt.Errorf("dependencies must be provided")
	}
	return NewRuntime(deps.WorkflowControl(), deps.SSEManager())
}

func (r *Runtime) SSEManager() ops.SSEManager {
	if r == nil {
		return nil
	}
	return r.sse
}

func (r *Runtime) ListPendingInputs(ctx context.Context, projectID string) ([]PendingInput, error) {
	jobs, _, err := r.ctl.ListJobs(ctx, swf.ListJobsRequest{
		Stores:    []swf.JobStore{swf.JobStoreActive},
		TenantIds: []string{projectID},
		Statuses:  []swf.JobStatus{swf.JobStatusReady},
		JobTasks: []swf.JobTaskFilter{{
			JobType:  "recipe",
			TaskType: "input:collect_user_input",
		}},
		PageSize: 1000,
	})
	if err != nil {
		return nil, err
	}

	out := make([]PendingInput, len(jobs))
	for i, job := range jobs {
		out[i] = PendingInput{Id: job.JobKey.JobId}
	}
	return out, nil
}

func (r *Runtime) GetDetails(ctx context.Context, projectID string, jobID string) (*UserInputDetails, error) {
	job, err := r.findJob(ctx, projectID, jobID)
	if err != nil {
		return nil, err
	}

	task, req, _, err := r.getOutput(ctx, projectID, jobID)
	if err != nil {
		return nil, err
	}

	form := InputForm{}
	if err := ops.DecodeWithJsonTags(req.OpOutput, &form); err != nil {
		return nil, err
	}

	_ = task
	return &UserInputDetails{
		JobId:     jobID,
		Status:    "pending",
		StartTime: job.CreatedAt,
		Form:      toOpenAPIInputFormConfig(form),
	}, nil
}

func (r *Runtime) SubmitResponse(ctx context.Context, projectID string, jobID string, output FormResponse) error {
	task, req, artifacts, err := r.getOutput(ctx, projectID, jobID)
	if err != nil {
		return err
	}

	userID := ""
	if output.UserId != nil {
		userID = *output.UserId
	}
	if userID == "" {
		userID = "anonymous"
	}

	var response any
	if output.Response != nil {
		response = *output.Response
	}

	opOut := Output{
		Response: response,
		Fields:   output.Fields,
		UserID:   userID,
	}
	sConv := structs.New(opOut)
	sConv.TagName = "json"
	opOutMap := sConv.Map()

	out := workerops.ActivityInvocationOutput{
		GitResult: req.GitResult,
		OpOutput:  opOutMap,
	}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, out)
	if err != nil {
		return err
	}
	outData, err := swf.NewTaskData(env, artifacts...)
	if err != nil {
		return err
	}
	if err := task.Finish(ctx, outData); err != nil {
		return err
	}

	if r.sse != nil {
		r.sse.Broadcast(ops.SSEEvent{
			Type: "input_completed",
			Data: map[string]interface{}{"jobId": jobID},
		})
	}
	return nil
}

func (r *Runtime) Cancel(ctx context.Context, projectID string, jobID string, reason string) error {
	if err := r.ctl.Cancel(ctx, swf.JobKey{TenantId: projectID, JobId: jobID}); err != nil {
		return err
	}
	if r.sse != nil {
		r.sse.Broadcast(ops.SSEEvent{
			Type: "input_cancelled",
			Data: map[string]interface{}{
				"jobId":  jobID,
				"reason": reason,
			},
		})
	}
	return nil
}

func (r *Runtime) findJob(ctx context.Context, projectID string, jobID string) (*workflowctl.JobItem, error) {
	jobs, _, err := r.ctl.ListJobs(ctx, swf.ListJobsRequest{
		Stores:    []swf.JobStore{swf.JobStoreActive},
		TenantIds: []string{projectID},
		JobKeys:   []swf.JobKey{{TenantId: projectID, JobId: jobID}},
		Statuses:  []swf.JobStatus{swf.JobStatusReady},
		JobTasks: []swf.JobTaskFilter{{
			JobType:  "recipe",
			TaskType: "input:collect_user_input",
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query workflow: %w", err)
	}
	if len(jobs) == 0 {
		return nil, ErrInputNotPending
	}
	job := jobs[0]
	if job.JobKey.TenantId != projectID {
		return nil, ErrInputNotPending
	}
	return &job, nil
}

func (r *Runtime) getOutput(ctx context.Context, projectID string, jobID string) (workflowctl.TaskHandle, workerops.ActivityInvocationOutput, []swf.Artifact, error) {
	task, err := r.ctl.GetWaitingTask(ctx, swf.JobKey{
		TenantId: projectID,
		JobId:    jobID,
	})
	if err != nil {
		return nil, workerops.ActivityInvocationOutput{}, nil, fmt.Errorf("failed to find job: %w", err)
	}

	td, err := task.Data()
	if err != nil {
		return nil, workerops.ActivityInvocationOutput{}, nil, fmt.Errorf("failed to get task data: %w", err)
	}

	data, err := td.GetData()
	if err != nil {
		return nil, workerops.ActivityInvocationOutput{}, nil, fmt.Errorf("failed to get task data: %w", err)
	}

	artifacts, err := td.GetArtifacts()
	if err != nil {
		return nil, workerops.ActivityInvocationOutput{}, nil, fmt.Errorf("failed to get task artifacts: %w", err)
	}

	var env coretask.OutputEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, workerops.ActivityInvocationOutput{}, nil, fmt.Errorf("failed to unmarshal task output envelope: %w", err)
	}
	if env.Version != coretask.OutputEnvelopeVersion {
		return nil, workerops.ActivityInvocationOutput{}, nil, fmt.Errorf("unsupported task output envelope version %d", env.Version)
	}
	if env.Kind != coretask.OutputKindActivityInvocationOutput {
		return nil, workerops.ActivityInvocationOutput{}, nil, fmt.Errorf("unexpected task output kind %q", env.Kind)
	}

	var out workerops.ActivityInvocationOutput
	if err := env.DecodePayload(&out); err != nil {
		return nil, workerops.ActivityInvocationOutput{}, nil, fmt.Errorf("failed to decode activity output payload: %w", err)
	}
	return task, out, artifacts, nil
}
