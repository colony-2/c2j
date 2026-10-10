package workjob

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type deferredHandoffWorker struct {
	release <-chan struct{}
	handoff chan<- struct{}
}

func (deferredHandoffWorker) Name() string { return "deferred-handoff" }

func (w deferredHandoffWorker) Run(ctx jobworkflow.JobContext, _ jobdb.JobData) (jobdb.JobData, error) {
	// Successful Yield exits the goroutine. The compiler reports handoff from
	// a defer, after the lease has been rescheduled. Hold that defer open to
	// exercise the ordering without relying on scheduler speed or sleeps.
	defer func() {
		<-w.release
		close(w.handoff)
	}()
	return nil, ctx.Yield(context.Background(), jobdb.RescheduleExecutionRequest{
		NextRoute: jobdb.Route{JobType: "deferred-handoff"},
	})
}

func TestRunOneWaitsForDeferredHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runtime := newRunOneRuntime(toy.New(), time.Minute)
		runtime.setCancel(cancel)
		release, handoff := make(chan struct{}), make(chan struct{})
		worker := deferredHandoffWorker{release: release, handoff: handoff}
		engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).
			WithWorkerTenantId("tenant").WithMaxActive(1).PlusWorkers(worker).BuildEngine()
		require.NoError(t, err)
		_, err = engine.SubmitJob(ctx, jobdb.SubmitJob{
			TenantId: "tenant", JobID: "deferred-handoff", JobType: worker.Name(),
			Data: jobdb.NewTaskDataOrPanic(map[string]any{}),
		})
		require.NoError(t, err)
		done := make(chan struct{})
		go func() {
			engine.Run(ctx)
			close(done)
		}()
		synctest.Wait()
		// No goroutine can progress until release is closed. Finalization must
		// not cancel the runner while the handoff callback is still pending.
		if !runtime.state().finalized {
			t.Error("worker did not reschedule its lease")
		}
		select {
		case <-done:
			t.Error("run any returned before the deferred handoff notification")
		default:
		}
		if ctx.Err() != nil {
			t.Error("run any canceled the worker before its deferred handoff notification")
		}
		close(release)
		<-done
		select {
		case <-handoff:
		default:
			t.Error("run any returned without the handoff notification")
		}
		require.True(t, runtime.state().stopped)
		require.Equal(t, "rescheduled", runtime.state().action)
	})
}
