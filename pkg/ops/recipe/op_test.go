package recipe

import (
	"context"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/ops"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/swf-go/pkg/swf"
)

func TestGetRecipeOutputSuccess(t *testing.T) {
	artifact := swf.NewArtifactFromBytes("child-art", []byte("data"))
	payload := workerops.ActivityInvocationOutput{OpOutput: map[string]interface{}{"value": "ok"}}
	jobData, err := swf.NewTaskData(payload, artifact)
	if err != nil {
		t.Fatalf("failed to build task data: %v", err)
	}

	ctl := &fakeWorkflowControl{
		jobResultFunc: func(ctx context.Context, key swf.JobKey) (swf.JobData, error) {
			if key.JobId != "job-1" || key.TenantId != "tenant" {
				return nil, errors.New("unexpected job key")
			}
			return jobData, nil
		},
	}
	deps := ops.NewOpDependenciesBuilder().
		WithWorkflowControl(ctl).
		WithJobTool(&fakeJobTool{key: swf.JobKey{TenantId: "tenant"}}).
		Build()

	out, err := getRecipeOutput(deps, context.Background(), StartedJob{JobId: "job-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Outputs["value"] != "ok" {
		t.Fatalf("unexpected output: %v", out.Outputs)
	}
	if got := deps.GetOutputArtifacts(); len(got) != 1 || got[0].Name() != "child-art" {
		t.Fatalf("expected artifact to be captured, got %v", got)
	}
}

func TestGetRecipeOutputJobResultError(t *testing.T) {
	ctl := &fakeWorkflowControl{
		jobResultFunc: func(ctx context.Context, key swf.JobKey) (swf.JobData, error) {
			return nil, errors.New("job result error")
		},
	}
	deps := ops.NewOpDependenciesBuilder().
		WithWorkflowControl(ctl).
		WithJobTool(&fakeJobTool{key: swf.JobKey{TenantId: "tenant"}}).
		Build()

	_, err := getRecipeOutput(deps, context.Background(), StartedJob{JobId: "job-1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGetRecipeOutputDataError(t *testing.T) {
	ctl := &fakeWorkflowControl{
		jobResultFunc: func(ctx context.Context, key swf.JobKey) (swf.JobData, error) {
			return &errorTaskData{dataErr: errors.New("data error")}, nil
		},
	}
	deps := ops.NewOpDependenciesBuilder().
		WithWorkflowControl(ctl).
		WithJobTool(&fakeJobTool{key: swf.JobKey{TenantId: "tenant"}}).
		Build()

	_, err := getRecipeOutput(deps, context.Background(), StartedJob{JobId: "job-1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGetRecipeOutputUnmarshalError(t *testing.T) {
	ctl := &fakeWorkflowControl{
		jobResultFunc: func(ctx context.Context, key swf.JobKey) (swf.JobData, error) {
			return &errorTaskData{data: []byte("not-json")}, nil
		},
	}
	deps := ops.NewOpDependenciesBuilder().
		WithWorkflowControl(ctl).
		WithJobTool(&fakeJobTool{key: swf.JobKey{TenantId: "tenant"}}).
		Build()

	_, err := getRecipeOutput(deps, context.Background(), StartedJob{JobId: "job-1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGetRecipeOutputArtifactsError(t *testing.T) {
	payload := workerops.ActivityInvocationOutput{OpOutput: map[string]interface{}{}}
	data, err := swf.NewTaskData(payload)
	if err != nil {
		t.Fatalf("failed to build task data: %v", err)
	}
	ctl := &fakeWorkflowControl{
		jobResultFunc: func(ctx context.Context, key swf.JobKey) (swf.JobData, error) {
			return &errorTaskData{data: data.GetDataOrPanic(), artifactsErr: errors.New("artifact error")}, nil
		},
	}
	deps := ops.NewOpDependenciesBuilder().
		WithWorkflowControl(ctl).
		WithJobTool(&fakeJobTool{key: swf.JobKey{TenantId: "tenant"}}).
		Build()

	_, err = getRecipeOutput(deps, context.Background(), StartedJob{JobId: "job-1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestWaitAndGetRecipeOutputAwaitError(t *testing.T) {
	ctl := &fakeWorkflowControl{}
	jobTool := &fakeJobTool{key: swf.JobKey{TenantId: "tenant"}, awaitErr: errors.New("await error")}
	deps := ops.NewOpDependenciesBuilder().
		WithWorkflowControl(ctl).
		WithJobTool(jobTool).
		Build()

	_, err := waitAndGetRecipeOutput(deps, context.Background(), StartedJob{JobId: "job-1"})
	if err == nil {
		t.Fatal("expected error")
	}
	if ctl.jobResultCalls != 0 {
		t.Fatalf("expected JobResult not called, got %d", ctl.jobResultCalls)
	}
}
