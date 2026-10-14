package recipetest

import (
	"context"
	"sync"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// Isolated recipe tests still use JobDB's scope policies, with task execution
// supplied by the mock/cassette harness. No second retry/timeout engine exists.
type isolatedScopeContext struct {
	jobworkflow.ExecutionContext
	tasks     *testJobContext
	callbacks *sync.WaitGroup
}

func (c isolatedScopeContext) DoTask(call jobworkflow.TaskCall, build func(context.Context) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	data, err := build(c.Context())
	if err != nil {
		return nil, err
	}
	out, _, err := c.tasks.doMockedTask(c.Context(), call.Policy, call.TaskType, data, true)
	return out, err
}
func (c isolatedScopeContext) DoScope(site string, opts jobworkflow.ScopeOptions, body func(jobworkflow.ExecutionContext) (jobdb.JobData, error)) (jobdb.JobData, error) {
	gate := newCallbackGate(c.callbacks)
	defer gate.close()
	opts.Catch = append([]jobworkflow.CatchRule(nil), opts.Catch...)
	for i := range opts.Catch {
		handle := opts.Catch[i].Handle
		opts.Catch[i].Handle = func(exec jobworkflow.ExecutionContext, err error) (jobworkflow.CatchResult, error) {
			if !gate.begin() {
				return jobworkflow.CatchResult{}, context.Canceled
			}
			defer c.callbacks.Done()
			return handle(exec, err)
		}
	}
	return c.ExecutionContext.DoScope(site, opts, func(inner jobworkflow.ExecutionContext) (jobdb.JobData, error) {
		if !gate.begin() {
			return nil, context.Canceled
		}
		defer c.callbacks.Done()
		return body(isolatedScopeContext{inner, c.tasks, c.callbacks})
	})
}

// A canceled engine invocation can return before its callback starts or ends.
// Track callbacks already running; prevent late callbacks from touching the harness.
type callbackGate struct {
	mu        sync.Mutex
	closed    bool
	callbacks *sync.WaitGroup
}

func newCallbackGate(callbacks *sync.WaitGroup) *callbackGate {
	callbacks.Add(1)
	return &callbackGate{callbacks: callbacks}
}
func (g *callbackGate) begin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.callbacks.Add(1)
	return true
}
func (g *callbackGate) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	g.callbacks.Done()
}

func runIsolatedScope(tasks *testJobContext, site string, opts jobworkflow.ScopeOptions, body func(jobworkflow.ExecutionContext) (jobdb.JobData, error)) (jobdb.JobData, error) {
	rt := toy.New()
	ctx := tasks.Context()
	key, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: tasks.jobKey.TenantId, JobType: "recipe-test", Data: jobdb.NewTaskDataOrPanic(nil)}})
	if err != nil {
		return nil, err
	}
	var out jobdb.JobData
	var bodyErr error
	var callbacks sync.WaitGroup
	gate := newCallbackGate(&callbacks)
	worker := jobworkflow.ExecutionWorker{JobType: "recipe-test", Execute: func(exec jobworkflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		if !gate.begin() {
			return nil, context.Canceled
		}
		defer callbacks.Done()
		out, bodyErr = (isolatedScopeContext{exec, tasks, &callbacks}).DoScope(site, opts, body)
		return out, bodyErr
	}}
	h, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key.JobKey, JobWorker: worker, WorkerID: "recipe-test", LeaseDuration: time.Minute, AwaitThreshold: time.Hour})
	if err != nil {
		gate.close()
		return nil, err
	}
	_, err = h.Run(nil)
	gate.close()
	callbacks.Wait()
	if err != nil {
		return nil, err
	}
	return out, bodyErr
}
