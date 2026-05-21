package recipe

import (
	"context"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/ops"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
)

func TestGetOpsIncludesAwaitResultSoft(t *testing.T) {
	for _, op := range GetOps() {
		if op.GetName() == "recipe.await_result_soft" {
			return
		}
	}
	t.Fatal("expected recipe.await_result_soft to be registered by recipe.GetOps")
}

func TestAwaitResultSoftCompletedChildReturnsOutput(t *testing.T) {
	artifact := swf.NewArtifactFromBytes("child-art", []byte("data"))
	swf.AssignArtifactKey(artifact, swf.ArtifactKey{JobId: "child-1", TaskOrdinal: 7, Name: "child-art", SizeBytes: 4})
	payload := workerops.ActivityInvocationOutput{OpOutput: map[string]interface{}{"value": "ok"}}
	jobData, err := swf.NewTaskData(payload, artifact)
	if err != nil {
		t.Fatalf("failed to build task data: %v", err)
	}
	ctl := &fakeWorkflowControl{
		inspectFunc: func(ctx context.Context, key swf.JobKey) (workflowctl.JobInspection, error) {
			return workflowctl.JobInspection{
				JobKey:      key,
				Terminal:    true,
				Status:      "completed",
				FailureKind: "none",
				Output:      jobData,
			}, nil
		},
	}
	deps := ops.NewOpDependenciesBuilder().
		WithWorkflowControl(ctl).
		WithJobTool(&fakeJobTool{key: swf.JobKey{TenantId: "tenant"}}).
		Build()

	out, err := awaitResultSoft(deps, context.Background(), AwaitResultSoftInput{JobID: "child-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != "completed" || !out.Terminal {
		t.Fatalf("unexpected status output: %#v", out)
	}
	if out.Outputs["value"] != "ok" {
		t.Fatalf("unexpected outputs: %#v", out.Outputs)
	}
	if _, ok := out.Artifacts["child-art"]; !ok {
		t.Fatalf("expected child artifact ref, got %#v", out.Artifacts)
	}
	if out.PartialOutputsAvailable || out.PartialArtifactsAvailable {
		t.Fatalf("completed output should not be marked partial: %#v", out)
	}
}

func TestAwaitResultSoftFailedChildReturnsDataNotError(t *testing.T) {
	artifact := swf.NewArtifactFromBytes("failure-log", []byte("data"))
	swf.AssignArtifactKey(artifact, swf.ArtifactKey{JobId: "child-1", TaskOrdinal: 7, Name: "failure-log", SizeBytes: 4})
	payload := workerops.ActivityInvocationOutput{OpOutput: map[string]interface{}{"ok": false, "reason": "bad"}}
	jobData, err := swf.NewTaskData(payload, artifact)
	if err != nil {
		t.Fatalf("failed to build task data: %v", err)
	}
	jobTool := &fakeJobTool{
		key:      swf.JobKey{TenantId: "tenant"},
		awaitErr: &swf.JobFailedError{Cause: swf.AppError{Payload: swf.AppErrorPayload{Message: "child failed"}}},
	}
	ctl := &fakeWorkflowControl{
		inspectFunc: func(ctx context.Context, key swf.JobKey) (workflowctl.JobInspection, error) {
			return workflowctl.JobInspection{
				JobKey:         key,
				Terminal:       true,
				Status:         "failed",
				FailureKind:    "task_error",
				FailureMessage: "child failed",
				Output:         jobData,
			}, nil
		},
	}
	deps := ops.NewOpDependenciesBuilder().
		WithWorkflowControl(ctl).
		WithJobTool(jobTool).
		Build()

	out, err := awaitResultSoft(deps, context.Background(), AwaitResultSoftInput{JobID: "child-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != "failed" || !out.Terminal || out.FailureKind != "task_error" {
		t.Fatalf("unexpected failed output: %#v", out)
	}
	if out.Outputs["reason"] != "bad" {
		t.Fatalf("unexpected partial outputs: %#v", out.Outputs)
	}
	if !out.PartialOutputsAvailable || !out.PartialArtifactsAvailable {
		t.Fatalf("expected partial availability flags: %#v", out)
	}
	if len(jobTool.awaitArgs) != 1 || jobTool.awaitArgs[0] != "child-1" {
		t.Fatalf("expected terminal await, got %#v", jobTool.awaitArgs)
	}
}

func TestAwaitResultSoftCurrentStatusDoesNotAwait(t *testing.T) {
	jobTool := &fakeJobTool{key: swf.JobKey{TenantId: "tenant"}}
	ctl := &fakeWorkflowControl{
		inspectFunc: func(ctx context.Context, key swf.JobKey) (workflowctl.JobInspection, error) {
			return workflowctl.JobInspection{JobKey: key, Terminal: false, Status: "running"}, nil
		},
	}
	deps := ops.NewOpDependenciesBuilder().
		WithWorkflowControl(ctl).
		WithJobTool(jobTool).
		Build()

	out, err := awaitResultSoft(deps, context.Background(), AwaitResultSoftInput{
		JobID:      "child-1",
		ReturnWhen: "current_status",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != "running" || out.Terminal {
		t.Fatalf("unexpected running output: %#v", out)
	}
	if len(jobTool.awaitArgs) != 0 {
		t.Fatalf("current_status should not await, got %#v", jobTool.awaitArgs)
	}
}

func TestAwaitResultSoftInspectErrorFailsOp(t *testing.T) {
	ctl := &fakeWorkflowControl{
		inspectFunc: func(ctx context.Context, key swf.JobKey) (workflowctl.JobInspection, error) {
			return workflowctl.JobInspection{}, errors.New("inspect failed")
		},
	}
	deps := ops.NewOpDependenciesBuilder().
		WithWorkflowControl(ctl).
		WithJobTool(&fakeJobTool{key: swf.JobKey{TenantId: "tenant"}}).
		Build()

	_, err := awaitResultSoft(deps, context.Background(), AwaitResultSoftInput{JobID: "child-1"})
	if err == nil || err.Error() != "inspect failed" {
		t.Fatalf("expected inspect error, got %v", err)
	}
}

func TestAwaitResultSoftFallbackSoftensJobFailed(t *testing.T) {
	ctl := &jobResultOnlyWorkflowControl{
		jobResultFunc: func(ctx context.Context, key swf.JobKey) (swf.JobData, error) {
			return nil, &swf.JobFailedError{Cause: swf.AppError{Payload: swf.AppErrorPayload{Message: "child failed"}}}
		},
	}
	deps := ops.NewOpDependenciesBuilder().
		WithWorkflowControl(ctl).
		WithJobTool(&fakeJobTool{key: swf.JobKey{TenantId: "tenant"}}).
		Build()

	out, err := awaitResultSoft(deps, context.Background(), AwaitResultSoftInput{JobID: "child-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != "failed" || !out.Terminal || out.FailureKind != "task_error" {
		t.Fatalf("unexpected fallback output: %#v", out)
	}
}

type jobResultOnlyWorkflowControl struct {
	jobResultFunc func(ctx context.Context, key swf.JobKey) (swf.JobData, error)
}

func (j *jobResultOnlyWorkflowControl) StartJob(ctx context.Context, req workflowctl.StartJob) (swf.JobKey, error) {
	return swf.JobKey{}, errors.New("not implemented")
}

func (j *jobResultOnlyWorkflowControl) Cancel(ctx context.Context, jobKey swf.JobKey) error {
	return errors.New("not implemented")
}

func (j *jobResultOnlyWorkflowControl) ListJobs(ctx context.Context, request swf.ListJobsRequest) ([]workflowctl.JobItem, string, error) {
	return nil, "", errors.New("not implemented")
}

func (j *jobResultOnlyWorkflowControl) CompleteTask(ctx context.Context, jobKey swf.JobKey, taskOrdinal int64, hash string, data any) error {
	return errors.New("not implemented")
}

func (j *jobResultOnlyWorkflowControl) GetWaitingTask(ctx context.Context, jobKey swf.JobKey) (workflowctl.TaskHandle, error) {
	return nil, errors.New("not implemented")
}

func (j *jobResultOnlyWorkflowControl) GetArtifactLazy(ctx context.Context, tenantId string, key swf.ArtifactKey) swf.Artifact {
	return key.ToLazyArtifact(nil, tenantId)
}

func (j *jobResultOnlyWorkflowControl) JobResult(ctx context.Context, key swf.JobKey) (swf.JobData, error) {
	if j.jobResultFunc == nil {
		return nil, errors.New("job result not configured")
	}
	return j.jobResultFunc(ctx, key)
}

var _ workflowctl.WorkflowControl = &jobResultOnlyWorkflowControl{}
