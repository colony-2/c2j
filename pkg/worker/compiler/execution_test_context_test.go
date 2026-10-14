package compiler

import (
	"context"
	"github.com/colony-2/c2j/pkg/swfutil"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type testTaskCall = jobworkflow.TaskCall
type testContext = context.Context
type testScopeOptions = jobworkflow.ScopeOptions
type testExecutionContext = jobworkflow.ExecutionContext

func testBackground() context.Context { return context.Background() }

// Compiler unit tests stub task outputs but run policy through the real JobDB
// scope engine. This avoids maintaining another catch/retry implementation.
type mockScopeContext struct {
	jobworkflow.ExecutionContext
	tasks jobworkflow.ExecutionContext
}

func (c mockScopeContext) DoTask(call jobworkflow.TaskCall, build func(context.Context) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	return c.tasks.DoTask(call, build)
}
func (c mockScopeContext) DoScope(site string, opts jobworkflow.ScopeOptions, body func(jobworkflow.ExecutionContext) (jobdb.JobData, error)) (jobdb.JobData, error) {
	return c.ExecutionContext.DoScope(site, opts, func(inner jobworkflow.ExecutionContext) (jobdb.JobData, error) {
		return body(mockScopeContext{inner, c.tasks})
	})
}
func runMockScope(tasks jobworkflow.ExecutionContext, site string, opts jobworkflow.ScopeOptions, body func(jobworkflow.ExecutionContext) (jobdb.JobData, error)) (jobdb.JobData, error) {
	rt := toy.New()
	ctx := context.Background()
	key, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "scope-tests", JobType: "scope", Data: jobdb.NewTaskDataOrPanic(nil)}})
	if err != nil {
		return nil, err
	}
	var result jobdb.JobData
	var bodyErr error
	worker := jobworkflow.ExecutionWorker{JobType: "scope", Execute: func(exec jobworkflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		result, bodyErr = mockScopeContext{exec, tasks}.DoScope(site, opts, body)
		return result, bodyErr
	}}
	handle, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key.JobKey, JobWorker: worker, WorkerID: "test", LeaseDuration: time.Minute, AwaitThreshold: time.Minute})
	if err != nil {
		return nil, err
	}
	_, err = handle.Run(nil)
	if err != nil {
		return nil, err
	}
	return result, bodyErr
}

func asExecutionTestWorker(value any) jobworkflow.ExecutionWorker {
	if w, ok := value.(jobworkflow.ExecutionWorker); ok {
		return w
	}
	w := value.(interface {
		Name() string
		Run(jobworkflow.ExecutionContext, jobdb.JobData) (jobdb.JobData, error)
	})
	return jobworkflow.ExecutionWorker{JobType: w.Name(), Execute: w.Run}
}
func testValueCall(ctx jobworkflow.ExecutionContext, p jobdb.RunPolicy, typ string, data jobdb.TaskData) (jobdb.TaskData, error) {
	raw, err := data.GetData()
	if err != nil {
		return nil, err
	}
	return ctx.DoTask(jobworkflow.TaskCall{Site: typ, TaskType: typ, ContractVersion: 1, Command: raw, Policy: p}, func(context.Context) (jobdb.TaskData, error) { return data, nil })
}

func (a *thinpackForwarder) doTask(_ jobdb.RunPolicy, typ string, data jobdb.TaskData, invoke func(jobdb.TaskData) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	data, err := a.prepareInput(typ, data)
	if err != nil {
		return nil, err
	}
	out, err := invoke(data)
	if err != nil || out == nil {
		return out, err
	}
	return a.recordOutput(out)
}

func executionTaskRuns(run jobdb.GetJobRunResponse) []jobdb.TaskRun {
	tasks, err := swfutil.ExecutionTasks(run)
	if err != nil {
		panic(err)
	}
	out := make([]jobdb.TaskRun, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.TaskRun)
	}
	return out
}
