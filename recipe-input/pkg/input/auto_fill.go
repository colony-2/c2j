package input

import (
	"context"
	"fmt"

	"github.com/colony-2/c2j/recipe-core/pkg/ops"
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
		AddStep(autoFillStepName, ops.NewStep(autoFillInput)).
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
