package recipe

import (
	"sync/atomic"

	"github.com/colony-2/c2j/pkg/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
)

type goalWaitTask struct {
	ctl   *workerworkflow.SWFWorkflowControl
	input StartedJob
	calls atomic.Int32
	soft  bool
}

func (*goalWaitTask) Name() string { return "goal-wait" }
func (t *goalWaitTask) Run(c wf.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	t.calls.Add(1)
	d := ops.NewOpDependenciesBuilder().WithWorkflowControl(t.ctl).WithJobTool(&ops.TaskBasedJobTool{TaskContext: c}).Build()
	if t.soft {
		out, err := awaitResultSoft(d, c.Context(), AwaitResultSoftInput{Target: WaitTarget{Job: t.input.JobID}})
		if err != nil {
			return nil, err
		}
		return jobdb.NewTaskData(out)
	}
	out, err := waitAndGetRecipeOutput(d, c.Context(), t.input)
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(out)
}
