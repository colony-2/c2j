package compiler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/swf-go/pkg/swf"
)

type validationJobContext struct {
	inner      swf.JobContext
	gitContext contextual.GitCommitContext
}

func (v *validationJobContext) AwaitJobs(jobIds ...string) error {
	if v.inner == nil {
		return nil
	}
	return v.inner.AwaitJobs(jobIds...)
}

func wrapValidationContext(ctx workflow.Context, commitContext contextual.GitCommitContext) workflow.Context {
	if _, ok := ctx.JobContext.(*validationJobContext); ok {
		return ctx
	}
	return workflow.Context{
		JobContext:           &validationJobContext{inner: ctx.JobContext, gitContext: commitContext},
		ServiceDependencies2: ctx.ServiceDependencies2,
	}
}

func (v *validationJobContext) GetJobKey() swf.JobKey {
	if v.inner == nil {
		return swf.JobKey{}
	}
	return v.inner.GetJobKey()
}

func (v *validationJobContext) Logger() *slog.Logger {
	if v.inner == nil {
		return slog.Default()
	}
	return v.inner.Logger()
}

func (v *validationJobContext) AwaitDuration(waitFor swf.Duration) error {
	if v.inner == nil {
		return nil
	}
	return v.inner.AwaitDuration(waitFor)
}

func (v *validationJobContext) DoTask(_ swf.RunPolicy, taskType string, data swf.TaskData) (swf.TaskData, error) {
	parts := strings.SplitN(taskType, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid task type: %s", taskType)
	}

	opName := parts[0]
	stepName := parts[1]
	op, exists := ops.Get(opName)
	if !exists {
		return nil, fmt.Errorf("operation %s not found", opName)
	}
	taskPrefix := op.GetMetadata().Type
	chain := op.TaskChain()
	stepIndex := -1
	var stepOutputType reflect.Type
	for i, step := range chain {
		if step.Name == stepName {
			stepIndex = i
			stepOutputType = step.OutputType
			break
		}
	}
	if stepIndex == -1 {
		return nil, fmt.Errorf("task step %s not found", stepName)
	}

	zeroOutput, err := zeroOutputFromType(stepOutputType)
	if err != nil {
		return nil, err
	}

	var invocation workerops.ActivityInvocationRequest
	payload, err := data.GetData()
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &invocation); err != nil {
		return nil, err
	}

	gitResult := v.gitContext
	if gitResult.ParentHash == "" {
		gitResult.ParentHash = invocation.GitTaskContext.ParentHash
	}
	if gitResult.PersistHash == "" {
		gitResult.PersistHash = invocation.GitTaskContext.PersistHash
	}

	nextTask := ""
	if stepIndex < len(chain)-1 {
		nextTask = fmt.Sprintf("%s:%s", taskPrefix, chain[stepIndex+1].Name)
	}

	if taskPrefix == "extension_execution" {
		var invocation workerops.ActivityInvocationRequest
		payload, err := data.GetData()
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &invocation); err != nil {
			return nil, err
		}
		selector, _ := invocation.Input["selector"].(string)
		if selector != "" {
			repoSource, _ := invocation.Input["repository_source"].(string)
			repoRef, _ := invocation.Input["repository_ref"].(string)
			resolved, _, err := loadSelectorOp(selector, extops.ResolveOptions{
				RepositorySource: repoSource,
				RepositoryRef:    repoRef,
			})
			if err != nil {
				return nil, err
			}
			zeroOutput = resolved.ZeroOutput()
		}
	}

	envelope := workerops.ActivityInvocationOutput{
		OpOutput:  zeroOutput,
		GitResult: gitResult,
		NextTask:  nextTask,
	}

	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, envelope)
	if err != nil {
		return nil, err
	}
	return swf.NewTaskData(env)
}
