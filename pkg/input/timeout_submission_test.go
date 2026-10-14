package input

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	c2workflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	workflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestInputSubmissionHonorsDeadlineBeforeWorkerPickup(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "before_deadline"
		if expired {
			name = "after_deadline"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				rt := toy.New()
				form := InputForm{Question: "Continue?", Type: "short_answer"}
				inner := (structuredWorker{forms: []InputForm{form}}).executionWorker()
				worker := workflow.ExecutionWorker{JobType: "recipe", Execute: func(c workflow.ExecutionContext, d jobdb.JobData) (jobdb.JobData, error) {
					return c.DoScope("input", workflow.ScopeOptions{TotalTimeout: time.Minute}, func(scoped workflow.ExecutionContext) (jobdb.JobData, error) { return inner.Execute(scoped, d) })
				}}
				submitted, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(nil)}})
				require.NoError(t, err)
				run := func() workflow.JobRunOutcome {
					h, err := workflow.GetJobForRun(ctx, rt, workflow.GetJobForRunRequest{JobKey: submitted.JobKey, JobWorker: worker, WorkerID: "test", LeaseDuration: time.Minute})
					require.NoError(t, err)
					out, err := h.Run(nil)
					require.NoError(t, err)
					return out
				}
				require.Equal(t, workflow.JobRunSuspended, run().Status)
				engine, err := workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, err)
				client, err := NewRuntime(&c2workflow.SWFWorkflowControl{Engine: engine}, nil)
				require.NoError(t, err)
				if expired {
					time.Sleep(2 * time.Minute)
				}
				var answer any = "yes"
				err = client.SubmitResponse(ctx, submitted.JobKey.TenantId, submitted.JobKey.JobId, FormResponse{Response: &answer})
				if expired {
					require.ErrorIs(t, err, jobdb.ErrConflict, "expiry rejects completion even before another worker runs")
					require.Equal(t, workflow.JobRunFailed, run().Status)
				} else {
					require.NoError(t, err)
					require.Equal(t, workflow.JobRunCompleted, run().Status)
				}
			})
		})
	}
}
