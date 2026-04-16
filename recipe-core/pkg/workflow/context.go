package workflow

import (
	"log/slog"

	"github.com/colony-2/c2j/recipe-core/pkg/ops"
	"github.com/colony-2/swf-go/pkg/swf"
)

type Context struct {
	swf.JobContext
	ops.ServiceDependencies2
}

func (c Context) GetJobId() string {
	return c.JobContext.GetJobKey().JobId
}

func (c Context) GetLogger() *slog.Logger {
	return slog.Default()
}
