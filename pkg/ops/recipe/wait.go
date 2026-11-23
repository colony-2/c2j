package recipe

import (
	"context"
	"fmt"

	"github.com/colony-2/c2j/pkg/ops"
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
		p.On = "job"
	}
	if p.Condition == "" {
		p.Condition = "success"
		if p.On == "job" && !result {
			p.Condition = "complete"
		}
	}
	if p.On != "job" {
		return p, fmt.Errorf("goal waits are unsupported; use wait.on: job and target.job")
	}
	if p.Condition != "success" && p.Condition != "complete" {
		return p, fmt.Errorf("wait.condition must be success or complete")
	}
	if result && p.Condition != "success" {
		return p, fmt.Errorf("result waits require success")
	}
	return p, nil
}

type ResultSource struct {
	Goal string `json:"goal,omitempty"`
	Job  string `json:"job"`
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
	policy, err := in.Wait.normalized(result)
	if err != nil {
		return zero, err
	}
	if in.Target.Goal != "" {
		return zero, fmt.Errorf("goal targets are unsupported; use target.job")
	}
	if in.Target.Job != "" {
		in.JobID = in.Target.Job
	}
	target := jobdb.DependencyTarget{JobID: in.JobID}
	dependency := jobdb.Dependency{Target: target, Condition: jobdb.JobPrereqCondition(policy.Condition)}
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
			if result && bindings[0].State == jobdb.DependencyFailed && deps.WorkflowControl() != nil {
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

func (p WaitPolicy) forSubmission(grouped, result bool) (WaitPolicy, error) {
	if grouped && p.On == "" {
		p.On = "job"
	}
	return p.normalized(result)
}
