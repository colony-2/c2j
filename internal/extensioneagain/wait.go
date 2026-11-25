package extensioneagain

import (
	"context"
	"errors"

	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
)

type waitKey struct{}

func WithWait(ctx context.Context, wait func(context.Context, wf.TaskContext) error) context.Context {
	return context.WithValue(ctx, waitKey{}, wait)
}

func Wait(local context.Context, task wf.TaskContext) error {
	wait, ok := local.Value(waitKey{}).(func(context.Context, wf.TaskContext) error)
	if !ok {
		return jobdb.ClassifyExecutionInfrastructure(errors.New("extension EAGAIN wait is unsupported"), jobdb.ExecutionInfrastructureRuntimeIO, "c2j.extension_eagain", nil, nil)
	}
	return wait(local, task)
}
