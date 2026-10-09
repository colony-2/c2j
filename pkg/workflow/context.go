package workflow

import (
	"context"
	"log/slog"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// TaskHistoryReader provides read-only access for verified legacy replay recovery.
type TaskHistoryReader interface {
	CachedTaskOutput(context.Context, jobdb.JobKey, string, int64, string) (jobdb.TaskData, error)
}

type Context struct {
	JobPackages        []string
	TaskHistory        TaskHistoryReader
	WorkspaceSnapshots bool
	SuspendExecution   func(jobworkflow.JobContext, string, execution.Requirements) error
	StageNodeExecution func(execution.Requirements)
	jobworkflow.JobContext
	ops.ServiceDependencies2
}

func (c Context) GetJobId() string {
	return c.JobContext.GetJobKey().JobId
}

func (c Context) GetLogger() *slog.Logger {
	return slog.Default()
}
