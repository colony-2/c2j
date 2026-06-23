package recipe

import (
	"context"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func TestAwaitChildGroupTreatsTerminalChildErrorsAsCollectable(t *testing.T) {
	jobTool := &fakeJobTool{
		key:      jobdb.JobKey{TenantId: "tenant", JobId: "parent"},
		awaitErr: &jobdb.JobFailedError{Cause: jobdb.AppError{Payload: jobdb.AppErrorPayload{Message: "child failed"}}},
	}
	deps := ops.NewOpDependenciesBuilder().WithJobTool(jobTool).Build()

	state := ChildGroupStepState{Children: []ChildGroupChildRecord{
		{Status: "started", JobID: "child-1"},
	}}
	out, err := awaitChildGroup(deps, context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected await error: %v", err)
	}
	if len(out.Children) != 1 || out.Children[0].JobID != "child-1" {
		t.Fatalf("unexpected state returned: %#v", out)
	}
	if len(jobTool.awaitArgs) != 1 || jobTool.awaitArgs[0] != "child-1" {
		t.Fatalf("unexpected await args: %#v", jobTool.awaitArgs)
	}
}

func TestAwaitChildGroupPropagatesSystemAwaitError(t *testing.T) {
	deps := ops.NewOpDependenciesBuilder().
		WithJobTool(&fakeJobTool{key: jobdb.JobKey{TenantId: "tenant"}, awaitErr: errors.New("backend down")}).
		Build()

	_, err := awaitChildGroup(deps, context.Background(), ChildGroupStepState{Children: []ChildGroupChildRecord{
		{Status: "started", JobID: "child-1"},
	}})
	if err == nil || err.Error() != "backend down" {
		t.Fatalf("expected backend error, got %v", err)
	}
}

func TestCollectChildGroupSoftensOnlyTerminalChildErrors(t *testing.T) {
	ctl := &fakeWorkflowControl{
		jobResultFunc: func(ctx context.Context, key jobdb.JobKey) (jobdb.JobData, error) {
			return nil, &jobdb.JobFailedError{Cause: jobdb.AppError{Payload: jobdb.AppErrorPayload{Message: "child failed"}}}
		},
	}
	deps := ops.NewOpDependenciesBuilder().
		WithWorkflowControl(ctl).
		WithJobTool(&fakeJobTool{key: jobdb.JobKey{TenantId: "tenant"}}).
		Build()

	out, err := collectChildGroup(deps, context.Background(), ChildGroupStepState{Children: []ChildGroupChildRecord{
		{Key: "required", Required: true, Status: "started", JobID: "child-1"},
		{Key: "optional", Required: false, Status: "started", JobID: "child-2"},
	}})
	if err != nil {
		t.Fatalf("unexpected collect error: %v", err)
	}
	if out.Ok {
		t.Fatalf("required failure should make group not ok: %#v", out)
	}
	if out.Summary.Failed != 2 || out.Summary.FailedRequired != 1 || out.Summary.FailedOptional != 1 {
		t.Fatalf("unexpected summary: %#v", out.Summary)
	}
	if len(out.BlockingIssues) != 1 || len(out.Warnings) != 1 {
		t.Fatalf("expected one blocking issue and one warning, got blocking=%#v warnings=%#v", out.BlockingIssues, out.Warnings)
	}
}

func TestCollectChildGroupPropagatesUnexpectedResultError(t *testing.T) {
	ctl := &fakeWorkflowControl{
		jobResultFunc: func(ctx context.Context, key jobdb.JobKey) (jobdb.JobData, error) {
			return nil, errors.New("result store down")
		},
	}
	deps := ops.NewOpDependenciesBuilder().
		WithWorkflowControl(ctl).
		WithJobTool(&fakeJobTool{key: jobdb.JobKey{TenantId: "tenant"}}).
		Build()

	_, err := collectChildGroup(deps, context.Background(), ChildGroupStepState{Children: []ChildGroupChildRecord{
		{Key: "child", Required: true, Status: "started", JobID: "child-1"},
	}})
	if err == nil || err.Error() != "result store down" {
		t.Fatalf("expected result store error, got %v", err)
	}
}

func TestReviewPackOptionalBlockingIssuesBecomeWarnings(t *testing.T) {
	out, err := buildChildGroupOutput(nil, ChildGroupStepState{
		Aggregate: ChildGroupAggregateConfig{Shape: "review_pack"},
		Children: []ChildGroupChildRecord{
			{
				Key:      "required",
				Required: true,
				Status:   "completed",
				Outputs: map[string]interface{}{
					"blocking_issues": []interface{}{map[string]interface{}{"message": "required block"}},
				},
			},
			{
				Key:      "optional",
				Required: false,
				Status:   "completed",
				Outputs: map[string]interface{}{
					"blocking_issues": []interface{}{map[string]interface{}{"message": "optional block"}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected build error: %v", err)
	}
	if out.Ok {
		t.Fatalf("required blocking issue should make group not ok")
	}
	if len(out.BlockingIssues) != 1 || out.BlockingIssues[0]["child"] != "required" {
		t.Fatalf("unexpected blocking issues: %#v", out.BlockingIssues)
	}
	if len(out.Warnings) != 1 || out.Warnings[0]["child"] != "optional" {
		t.Fatalf("unexpected warnings: %#v", out.Warnings)
	}
	if out.Aggregate["ok"] != false {
		t.Fatalf("expected aggregate ok=false, got %#v", out.Aggregate)
	}
}
