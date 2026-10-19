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

func TestInputSubmissionRacesTimeoutPickup(t *testing.T) {
	for _, scenario := range []string{"before_deadline", "late_response_first", "timeout_first"} {
		t.Run(scenario, func(t *testing.T) {
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
				if scenario != "before_deadline" {
					time.Sleep(2 * time.Minute)
				}
				// Expiry makes timeout work eligible; the pending response can
				// still win until that work is claimed by a worker.
				if scenario == "timeout_first" {
					require.Equal(t, workflow.JobRunFailed, run().Status)
				}
				var answer any = "yes"
				err = client.SubmitResponse(ctx, submitted.JobKey.TenantId, submitted.JobKey.JobId, FormResponse{Response: &answer})
				if scenario == "timeout_first" {
					require.Error(t, err, "a timeout winner rejects a later response")
				} else {
					require.NoError(t, err)
					outcome := run()
					if scenario == "late_response_first" {
						// Accepting the response does not extend the enclosing
						// scope's independent total deadline.
						require.Equal(t, workflow.JobRunFailed, outcome.Status)
						var timeout jobdb.TimeoutError
						require.ErrorAs(t, outcome.JobError, &timeout)
					} else {
						require.Equal(t, workflow.JobRunCompleted, outcome.Status)
					}
				}
			})
		})
	}
}
