package recipe

import (
	"context"
	"fmt"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type WaitTarget struct {
	Job  string `json:"job,omitempty"`
	Goal string `json:"goal,omitempty"`
}
type WaitPolicy struct {
	On        string `json:"on,omitempty"`
	Condition string `json:"condition,omitempty"`
}

func (p WaitPolicy) normalized(result bool) (WaitPolicy, error) {
	if p.On == "" {
		p.On = "goal"
	}
	if p.Condition == "" {
		p.Condition = "success"
		if p.On == "job" && !result {
			p.Condition = "complete"
		}
	}
	if p.On != "goal" && p.On != "job" {
		return p, fmt.Errorf("wait.on must be goal or job")
	}
	if p.Condition != "success" && p.Condition != "complete" {
		return p, fmt.Errorf("wait.condition must be success or complete")
	}
	if (p.On == "goal" || result) && p.Condition != "success" {
		return p, fmt.Errorf("goal and result waits require success")
	}
	return p, nil
}

type ResultSource struct {
	Goal string `json:"goal,omitempty"`
	Job  string `json:"job"`
}
type goalInspector interface {
	GetGoal(context.Context, jobdb.GoalKey) (jobdb.GoalInfo, error)
}
type dependencyAwaiter interface {
	AwaitDependencies(...jobdb.Dependency) ([]jobdb.DependencyResolution, error)
}

func awaitChild(deps ops.OpDependencies, ctx context.Context, in StartedJob, result bool) (jobdb.DependencyResolution, error) {
	var zero jobdb.DependencyResolution
	if deps == nil || deps.JobTool() == nil {
		return zero, fmt.Errorf("wait requires job tool")
	}
	if in.TenantID != "" && in.TenantID != deps.JobTool().GetJobKey().TenantId {
		return zero, fmt.Errorf("cross-tenant waits are not supported")
	}
	on := in.Wait.On
	if on == "" {
		on = "goal"
	}
	condition := in.Wait.Condition
	if condition == "" {
		condition = "success"
		if on == "job" && !result {
			condition = "complete"
		}
	}
	if on == "job" && in.Target.Goal != "" {
		return zero, fmt.Errorf("exact job wait requires target.job")
	}
	if on != "goal" && on != "job" {
		return zero, fmt.Errorf("wait.on must be goal or job")
	}
	if result && condition != "success" {
		return zero, fmt.Errorf("result waits require success")
	}
	if in.Target.Job != "" && in.Target.Goal != "" {
		return zero, fmt.Errorf("target requires exactly one job or goal")
	}
	if in.Target.Job != "" {
		in.JobID = in.Target.Job
	}
	if in.Target.Goal != "" {
		in.GoalID = in.Target.Goal
	}
	target := jobdb.DependencyTarget{}
	if on == "goal" {
		if in.GoalID == "" && in.JobID != "" {
			inspector, ok := deps.WorkflowControl().(workflowctl.JobInspector)
			if !ok {
				return zero, fmt.Errorf("job inspection unavailable")
			}
			info, err := inspector.InspectJob(ctx, jobdb.JobKey{TenantId: deps.JobTool().GetJobKey().TenantId, JobId: in.JobID})
			if err != nil {
				return zero, err
			}
			in.GoalID = info.GoalID
		}
		target.GoalID = in.GoalID
	} else {
		target.JobID = in.JobID
	}
	dependency := jobdb.Dependency{Target: target, Condition: jobdb.JobPrereqCondition(condition)}
	if err := jobdb.ValidateDependencies([]jobdb.Dependency{dependency}); err != nil {
		return zero, err
	}
	waiter, ok := deps.JobTool().(dependencyAwaiter)
	if !ok {
		return zero, fmt.Errorf("structured dependency waits unavailable")
	}
	bindings, err := waiter.AwaitDependencies(dependency)
	if err != nil {
		if len(bindings) == 1 {
			if result && on == "job" && bindings[0].State == jobdb.DependencyFailed && deps.WorkflowControl() != nil {
				_, resultErr := deps.WorkflowControl().JobResult(ctx, jobdb.JobKey{TenantId: deps.JobTool().GetJobKey().TenantId, JobId: target.JobID})
				if resultErr != nil {
					return bindings[0], resultErr
				}
			}
			return bindings[0], err
		}
		return zero, err
	}
	if len(bindings) != 1 || bindings[0].SatisfiedByJobID == "" || bindings[0].Target != target {
		return zero, fmt.Errorf("missing dependency binding")
	}
	return bindings[0], nil
}
