package workflow

import (
	"github.com/colony-2/c2j/pkg/execution"
	"log/slog"

	"github.com/colony-2/c2j/pkg/ops"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type Context struct {
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
