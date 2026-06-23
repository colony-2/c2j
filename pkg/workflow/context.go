package workflow

import (
	"log/slog"

	"github.com/colony-2/c2j/pkg/ops"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type Context struct {
	jobworkflow.JobContext
	ops.ServiceDependencies2
}

func (c Context) GetJobId() string {
	return c.JobContext.GetJobKey().JobId
}

func (c Context) GetLogger() *slog.Logger {
	return slog.Default()
}
