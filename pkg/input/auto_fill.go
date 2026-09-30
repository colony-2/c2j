package input

import (
	"context"
	"fmt"

	"github.com/colony-2/c2j/pkg/ops"
)

const (
	autoFillOpType   = "auto-fill-input"
	autoFillStepName = "echo"
	autoFillTaskType = autoFillOpType + ":" + autoFillStepName
)

// GetAutoFillOp registers the op that echoes a pre-filled input response.
func GetAutoFillOp() ops.RegisterableOp {
	op, err := ops.NewOp().
		WithType(autoFillOpType).
		AddStep(autoFillStepName, ops.NewStepWithDeps(autoFillOutputMap)).
		Build()
	if err != nil {
		panic(err)
	}
	return op
}

func autoFillInput(_ context.Context, form InputForm) (Output, error) {
	if form.Output == nil {
		return Output{}, fmt.Errorf("auto-fill-input requires form.output")
	}
	return *form.Output, nil
}

// Structured autofill uses the same schema and attachment validation as external input.
func autoFillInputWithDeps(deps ops.OpDependencies, ctx context.Context, form InputForm) (Output, error) {
	if form.Kind == "review" {
		return autoFillReview(deps, ctx, form)
	}
	if form.ResponseSchema == nil {
		return autoFillInput(ctx, form)
	}
	if form.Output == nil {
		return Output{}, fmt.Errorf("structured autofill requires an output")
	}
	if len(form.Output.Fields) > 0 || form.Output.Receipt != nil || form.Output.UserID != "" || len(form.Output.Metadata) > 0 {
		return Output{}, fmt.Errorf("structured autofill accepts response and artifact_refs only")
	}
	b := newArtifactBinder(ctx, deps, nil)
	transferred := false
	defer func() {
		if !transferred {
			b.cleanup()
		}
	}()
	out, err := acceptStructured(form, StructuredSubmission{RequestID: form.RequestID, SubmissionID: "autofill:" + form.RequestID, Response: form.Output.Response, ArtifactRefs: form.Output.ArtifactRefs}, Actor{ID: "autofill", Kind: "automation"}, b)
	if err != nil {
		return Output{}, err
	}
	for _, a := range b.artifacts {
		if err := deps.AddOutputArtifact(a); err != nil {
			return Output{}, err
		}
	}
	transferred = true
	return out, nil
}

// Use JSON mapping so receipts retain their timestamp representation.
func autoFillOutputMap(deps ops.OpDependencies, ctx context.Context, form InputForm) (map[string]any, error) {
	out, err := autoFillInputWithDeps(deps, ctx, form)
	if err != nil {
		return nil, err
	}
	value, err := jsonValue(out)
	if err != nil {
		return nil, err
	}
	return value.(map[string]any), nil
}
