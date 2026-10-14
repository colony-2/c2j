package executionruntime

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type gatedYieldContext struct {
	jobworkflow.ExecutionContext
	release  <-chan struct{}
	yielded  chan struct{}
	finished chan struct{}
}

func (c gatedYieldContext) DoScope(site string, opts jobworkflow.ScopeOptions, body func(jobworkflow.ExecutionContext) (jobdb.JobData, error)) (jobdb.JobData, error) {
	return c.ExecutionContext.DoScope(site, opts, func(inner jobworkflow.ExecutionContext) (jobdb.JobData, error) {
		child := c
		child.ExecutionContext = inner
		return body(child)
	})
}

func (c gatedYieldContext) Yield(ctx context.Context, req jobworkflow.YieldRequest) error {
	err := c.ExecutionContext.Yield(ctx, req)
	if jobworkflow.IsExecutionSuspended(err) {
		close(c.yielded)
		<-c.release
		close(c.finished)
	}
	return err
}

func TestEnvironmentReceiptAvailableBeforeYieldCallerReturns(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newFixture(t, backend, "2Gi", &execution.Requirements{Resources: execution.Resources{Memory: str("16Gi")}})
			rt := New(f.runtime, allocation("2Gi"))
			worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{Allocation: rt.Allocation, StageExecution: rt.Stage})
			execute := worker.Execute
			release, yielded, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer close(release)
			worker.Execute = func(ctx jobworkflow.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
				return execute(gatedYieldContext{ctx, release, yielded, finished}, in)
			}
			runnable, err := jobworkflow.GetJobForRun(context.Background(), rt, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: worker, TaskWorkers: f.tasks, WorkerID: "receipt", LeaseDuration: time.Minute})
			require.NoError(t, err)
			out, err := runnable.Run(nil)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			<-yielded
			select {
			case <-finished:
				t.Fatal("Yield caller already returned")
			default:
			}
			event, err := compiler.ExecutionHandoffFromOutcome(out)
			require.NoError(t, err)
			require.NotNil(t, event)
			require.Equal(t, f.key, event.JobKey)
			require.Equal(t, "16Gi", *event.Demand.Effective.Resources.Memory)
			require.True(t, event.Published)
			require.EqualValues(t, 1, f.first.Load())
			require.Zero(t, f.second.Load())
			saved, ok := runnable.Outcome()
			require.True(t, ok)
			receipt, err := compiler.ExecutionHandoffFromOutcome(saved)
			require.NoError(t, err)
			require.Equal(t, event, receipt)
		})
	}
}
