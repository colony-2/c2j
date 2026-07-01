package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/input/formdefaults"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type validationJobContext struct {
	inner              jobworkflow.JobContext
	gitContext         contextual.GitCommitContext
	resolvedSelectors  map[string]string
	resolvedGitRefs    map[string]string
	inputInitialInputs map[string]map[string]interface{}
}

type validationTaskOverride interface {
	DoValidationTask(jobdb.RunPolicy, string, jobdb.TaskData) (jobdb.TaskData, bool, error)
}

func (v *validationJobContext) AwaitJobs(jobIds ...string) error {
	if v.inner == nil {
		return nil
	}
	return v.inner.AwaitJobs(jobIds...)
}

func (v *validationJobContext) SubmitJob(context.Context, jobdb.SubmitJob) (jobdb.JobKey, error) {
	return jobdb.JobKey{}, fmt.Errorf("submitting jobs is not supported during validation")
}

func (v *validationJobContext) SubmitRestartJob(context.Context, jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	return jobdb.JobKey{}, fmt.Errorf("submitting restart jobs is not supported during validation")
}

func wrapValidationContext(ctx workflow.Context, commitContext contextual.GitCommitContext, resolvedSelectors map[string]string, resolvedGitRefs map[string]string) workflow.Context {
	if _, ok := ctx.JobContext.(*validationJobContext); ok {
		return ctx
	}
	return workflow.Context{
		JobContext: &validationJobContext{
			inner:              ctx.JobContext,
			gitContext:         commitContext,
			resolvedSelectors:  resolvedSelectors,
			resolvedGitRefs:    resolvedGitRefs,
			inputInitialInputs: map[string]map[string]interface{}{},
		},
		ServiceDependencies2: ctx.ServiceDependencies2,
	}
}

func (v *validationJobContext) GetJobKey() jobdb.JobKey {
	if v.inner == nil {
		return jobdb.JobKey{}
	}
	return v.inner.GetJobKey()
}

func (v *validationJobContext) Logger() *slog.Logger {
	if v.inner == nil {
		return slog.Default()
	}
	return v.inner.Logger()
}

func (v *validationJobContext) AwaitDuration(waitFor jobdb.Duration) error {
	if v.inner == nil {
		return nil
	}
	return v.inner.AwaitDuration(waitFor)
}

func (v *validationJobContext) executionTimeoutLimit() time.Duration {
	return activeExecutionTimeoutLimit(v.inner)
}

func (v *validationJobContext) DoTask(runPolicy jobdb.RunPolicy, taskType string, data jobdb.TaskData) (jobdb.TaskData, error) {
	if taskType == WithinRecipeResolutionTaskType {
		return newWithinRecipeResolutionTaskWorker().Run(jobworkflow.TaskContext{}, data)
	}

	parts := strings.SplitN(taskType, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid task type: %s", taskType)
	}

	opName := parts[0]
	stepName := parts[1]

	var invocation workerops.ActivityInvocationRequest
	payload, err := data.GetData()
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &invocation); err != nil {
		return nil, err
	}
	if override, ok := v.inner.(validationTaskOverride); ok {
		out, handled, err := override.DoValidationTask(runPolicy, taskType, data)
		if handled || err != nil {
			return out, err
		}
	}

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

	if taskPrefix == "input" {
		key := validationInputInvocationKey(invocation)
		switch stepName {
		case "generate_form":
			if v.inputInitialInputs != nil {
				v.inputInitialInputs[key] = cloneValidationInputMap(invocation.Input)
			}
		case "collect_user_input":
			if v.inputInitialInputs != nil {
				if initialInput, ok := v.inputInitialInputs[key]; ok {
					if synthesized, ok := formdefaults.ValidationOutputMap(initialInput); ok {
						zeroOutput = synthesized
					}
				}
			}
		default:
			if nextTask == "" {
				if synthesized, ok := formdefaults.ValidationOutputMap(invocation.Input); ok {
					zeroOutput = synthesized
				}
			}
		}
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
			selector = resolvedSelector(selector, v.resolvedSelectors)
			repoSource, _ := invocation.Input["repository_source"].(string)
			repoRef, _ := invocation.Input["repository_ref"].(string)
			if strings.TrimSpace(repoSource) == "" || strings.TrimSpace(repoRef) == "" {
				repoSource = strings.TrimSpace(invocation.GitTaskContext.RecipeSourceRepo)
				repoRef = strings.TrimSpace(invocation.GitTaskContext.RecipeSourceRef)
			}
			if strings.TrimSpace(repoSource) == "" || strings.TrimSpace(repoRef) == "" {
				repoSource = strings.TrimSpace(invocation.GitTaskContext.BaseRepo)
				repoRef = strings.TrimSpace(invocation.GitTaskContext.ResolvedBaseHash)
				if repoRef == "" {
					repoRef = strings.TrimSpace(invocation.GitTaskContext.BaseRef)
				}
			}
			if v.resolvedGitRefs == nil {
				v.resolvedGitRefs = map[string]string{}
			}
			resolved, _, err := loadSelectorOp(selector, extops.ResolveOptions{
				RepositorySource: repoSource,
				RepositoryRef:    repoRef,
				ResolvedRefs:     v.resolvedGitRefs,
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
		Jobs:      jobcontext.EmptyStartedJobsContext(),
	}

	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, envelope)
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(env)
}

func validationInputInvocationKey(invocation workerops.ActivityInvocationRequest) string {
	return fmt.Sprintf("%s::%d", invocation.GitTaskContext.NodePath, invocation.GitTaskContext.InvokeSeq)
}

func cloneValidationInputMap(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = cloneValidationValue(value)
	}
	return out
}

func cloneValidationValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneValidationInputMap(typed)
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = cloneValidationValue(item)
		}
		return out
	default:
		return typed
	}
}
