package task

import (
	"context"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// LocalWorker accepts transient dependencies supplied by c2j task middleware.
// The context belongs to this invocation; it is never part of recorded input
// or exposed to deterministic workflow code. JobDB's TaskContext retains all
// engine operations and identity. Decorators must forward both contexts.
type LocalWorker interface {
	RunTask(context.Context, jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error)
}

func RunLocal(worker jobworkflow.TaskWorker, ctx context.Context, tc jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	if local, ok := worker.(LocalWorker); ok {
		return local.RunTask(ctx, tc, input)
	}
	return worker.Run(tc, input)
}
