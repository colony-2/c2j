package workflow

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type Context struct {
	RecipeDigest       string
	CatchStateName     string
	ScopeState         func(*template.ResolutionContext)
	JobPackages        []string
	WorkspaceSnapshots bool
	SuspendExecution   func(jobworkflow.ExecutionContext, string, execution.Requirements) error
	StageNodeExecution func(execution.Requirements)
	JobContext         jobworkflow.ExecutionContext
	ops.ServiceDependencies2
}

func (c Context) GetJobId() string {
	return c.JobContext.GetJobKey().JobId
}

func (c Context) GetLogger() *slog.Logger {
	return slog.Default()
}

// DoTask delegates a logical call without constructing inputs on replay.
func (c Context) DoTask(call jobworkflow.TaskCall, build func(context.Context) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	return c.JobContext.DoTask(call, build)
}
func (c Context) GetJobKey() jobdb.JobKey { return c.JobContext.GetJobKey() }

// ValueCall is for control tasks whose declared request is already their stable command.
func ValueCall(ctx jobworkflow.ExecutionContext, site, typ string, policy jobdb.RunPolicy, command any) (jobdb.TaskData, error) {
	raw, err := json.Marshal(command)
	if err != nil {
		return nil, err
	}
	return ctx.DoTask(jobworkflow.TaskCall{Site: site, TaskType: typ, ContractVersion: 1, Command: raw, Policy: policy}, func(context.Context) (jobdb.TaskData, error) { return jobdb.NewTaskData(command) })
}
