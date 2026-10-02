//go:build timeoutrepro

package compiler

// Regression checks for JobDB timeout fixes and characterizations of remaining
// dependency-wait limitations. Run with -tags=timeoutrepro -run TestTimeoutRepro -v.

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestTimeoutReproJobDBExternalTaskDeadline(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend)
			const total = 100 * time.Millisecond
			const invocation = 600 * time.Millisecond
			key := submitTimeoutRepro(t, rt, "external", timeoutReproPolicy(5*time.Second, 2*time.Second))
			worker := timeoutReproWorker{"repro", func(ctx jobworkflow.JobContext, data jobdb.JobData) (jobdb.JobData, error) {
				return ctx.DoTask(timeoutReproPolicy(total, invocation), "external", data)
			}}
			require.Equal(t, jobworkflow.JobRunSuspended, runTimeoutRepro(t, rt, key, worker).Status)
			handoff := rt.lastHandoff(t)
			require.Equal(t, jobdb.Route{JobType: "repro", TaskType: "external"}, handoff.req.NextRoute)
			require.Equal(t, &jobdb.Route{JobType: "repro"}, handoff.req.AlternateRoute)
			require.NotNil(t, handoff.req.AlternateAfter)
			require.GreaterOrEqual(t, *handoff.req.AlternateAfter, time.Duration(0))
			require.LessOrEqual(t, *handoff.req.AlternateAfter, total,
				"external handoff must wake by the total deadline, not the invocation deadline")
			waitTimeoutRepro(t, handoff.at.Add(total+60*time.Millisecond))
			requireTimeoutCompletion(t, rt, key, runTimeoutRepro(t, rt, key, worker))
			require.Equal(t, handoff, rt.lastHandoff(t), "expired task must not be handed off again")
		})
	}
}

func TestTimeoutReproJobDeadlineEventuallyRecordsFailure(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend)
			key := submitTimeoutRepro(t, rt, "job-deadline", timeoutReproPolicy(100*time.Millisecond, 600*time.Millisecond))
			worker := timeoutReproWorker{"repro", func(ctx jobworkflow.JobContext, data jobdb.JobData) (jobdb.JobData, error) {
				return ctx.DoTask(jobdb.RunPolicy{}, "external", data)
			}}
			require.Equal(t, jobworkflow.JobRunSuspended, runTimeoutRepro(t, rt, key, worker).Status)
			handoff := rt.lastHandoff(t)
			require.NotNil(t, handoff.req.AlternateAfter)
			require.LessOrEqual(t, *handoff.req.AlternateAfter, 100*time.Millisecond)
			waitTimeoutRepro(t, handoff.at.Add(160*time.Millisecond))
			requireTimeoutCompletion(t, rt, key, runTimeoutRepro(t, rt, key, worker))
		})
	}
}

func TestTimeoutReproCoreAlternateRouteDoesNotOverrideWaits(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		for _, wait := range []string{"external-only", "dependency", "future"} {
			t.Run(backend+"/"+wait, func(t *testing.T) {
				rt := newTimeoutReproRuntime(t, backend)
				key := submitTimeoutRepro(t, rt, "parent", timeoutReproPolicy(time.Minute, time.Second))
				lease, err := rt.GetJobLease(context.Background(), jobdb.GetJobLeaseRequest{JobKey: key, Routes: []jobdb.Route{{JobType: "repro"}}, WorkerID: "setup", LeaseDuration: time.Second})
				require.NoError(t, err)
				require.NotNil(t, lease)
				defer lease.StopKeepAlive()
				after := 80 * time.Millisecond
				req := jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "repro", TaskType: "external"}, TaskWait: &jobdb.TaskWait{InputOrdinal: 0, OutputOrdinal: 1, ResumeJobType: "repro", InputHash: "probe"}, AlternateRoute: &jobdb.Route{JobType: "repro"}, AlternateAfter: &after}
				if wait == "dependency" {
					child := submitTimeoutRepro(t, rt, "child", timeoutReproPolicy(time.Minute, time.Second))
					req.WaitForJobIDs = []string{child.JobId}
				}
				if wait == "future" {
					future := time.Now().Add(time.Minute)
					req.WaitUntil = &future
				}
				require.NoError(t, lease.Reschedule(context.Background(), req))
				waitTimeoutRepro(t, time.Now().Add(after+50*time.Millisecond))
				found, err := rt.GetJobLease(context.Background(), jobdb.GetJobLeaseRequest{JobKey: key, Routes: []jobdb.Route{{JobType: "repro"}}, WorkerID: "timeout-worker", LeaseDuration: time.Second})
				require.NoError(t, err)
				if wait == "external-only" {
					require.NotNil(t, found)
					defer found.StopKeepAlive()
					require.NoError(t, found.Reschedule(context.Background(), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "repro"}}))
					t.Log("CONTROL: due alternate route makes an external-task wait retrievable without the external task worker")
				} else {
					require.Nil(t, found)
					info, err := rt.GetJob(context.Background(), key)
					require.NoError(t, err)
					leases, err := rt.PollWork(context.Background(), jobdb.PollWorkRequest{TenantId: "timeout-repro", WorkerID: "poller", Routes: []jobdb.Route{{JobType: "repro"}}, MetadataEquals: []jobdb.MetadataPredicate{}, Limit: 10, LeaseDuration: time.Second})
					require.NoError(t, err)
					for _, l := range leases {
						require.NotEqual(t, key, l.Job().JobKey)
						l.StopKeepAlive()
					}
					t.Logf("GAP: due alternate route cannot override %s wait; parent status=%s; neither targeted lease nor poll returns parent", wait, info.Status)
				}
			})
		}
	}
}

func TestTimeoutReproAwaitJobsDoesNotScheduleDeadline(t *testing.T) {
	rt := newTimeoutReproRuntime(t, "remote")
	child := submitTimeoutRepro(t, rt, "child", timeoutReproPolicy(time.Minute, time.Second))
	parent := submitTimeoutRepro(t, rt, "parent", timeoutReproPolicy(100*time.Millisecond, time.Second))
	worker := timeoutReproWorker{"repro", func(ctx jobworkflow.JobContext, data jobdb.JobData) (jobdb.JobData, error) {
		return data, ctx.AwaitJobs(child.JobId)
	}}
	require.Equal(t, jobworkflow.JobRunSuspended, runTimeoutRepro(t, rt, parent, worker).Status)
	h := rt.lastHandoff(t)
	require.Nil(t, h.req.AlternateRoute)
	require.Nil(t, h.req.WaitUntil)
	waitTimeoutRepro(t, h.at.Add(180*time.Millisecond))
	require.False(t, runTimeoutRepro(t, rt, parent, worker).LeaseAcquired)
	t.Log("BUG: AwaitJobs publishes dependency IDs but no deadline wake-up; timed-out parent remains ineligible")
}

func TestTimeoutReproJobDeadlinePreservesExistingChapter(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend)
			key := submitTimeoutRepro(t, rt, "cached-task", timeoutReproPolicy(100*time.Millisecond, 600*time.Millisecond))
			data, err := jobdb.NewTaskData(map[string]any{"cached": true})
			require.NoError(t, err)
			local := &timeoutReproActivity{name: "local", output: data}
			worker := timeoutReproWorker{"repro", func(ctx jobworkflow.JobContext, input jobdb.JobData) (jobdb.JobData, error) {
				result, err := ctx.DoTask(jobdb.RunPolicy{}, "local", input)
				if err != nil {
					return nil, err
				}
				return ctx.DoTask(jobdb.RunPolicy{}, "external", result)
			}}
			require.Equal(t, jobworkflow.JobRunSuspended, runTimeoutRepro(t, rt, key, worker, local).Status)
			require.EqualValues(t, 1, local.runs.Load())
			before, err := rt.ListChapters(context.Background(), jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			h := rt.lastHandoff(t)
			waitTimeoutRepro(t, h.at.Add(*h.req.AlternateAfter+50*time.Millisecond))
			runnable, err := jobworkflow.GetJobForRun(context.Background(), rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: []jobworkflow.TaskWorker{local}, WorkerID: "restart", LeaseDuration: time.Second})
			require.NoError(t, err)
			out, err := runnable.Run(nil)
			require.NoError(t, err)
			requireTimeoutCompletion(t, rt, key, out)
			after, err := rt.ListChapters(context.Background(), jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			require.Greater(t, len(after), len(before))
			require.Equal(t, before, after[:len(before)], "timeout completion must preserve existing history")
			require.EqualValues(t, 1, local.runs.Load())
		})
	}
}

func TestTimeoutReproAwaitDurationClampsToDeadline(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend)
			start := time.Now()
			key := submitTimeoutRepro(t, rt, "timed-wait", timeoutReproPolicy(100*time.Millisecond, time.Second))
			worker := timeoutReproWorker{"repro", func(ctx jobworkflow.JobContext, data jobdb.JobData) (jobdb.JobData, error) {
				return data, ctx.AwaitDuration(jobdb.Duration(time.Second))
			}}
			// This control specifically exercises durable timed suspension.
			runnable, err := jobworkflow.GetJobForRun(context.Background(), rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: worker, WorkerID: "timed-wait", LeaseDuration: time.Second, AwaitThreshold: time.Millisecond})
			require.NoError(t, err)
			out, err := runnable.Run(nil)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			h := rt.lastHandoff(t)
			require.NotNil(t, h.req.WaitUntil)
			require.Less(t, h.req.WaitUntil.Sub(start), 200*time.Millisecond)
			waitTimeoutRepro(t, h.req.WaitUntil.Add(50*time.Millisecond))
			requireTimeoutCompletion(t, rt, key, runTimeoutRepro(t, rt, key, worker))
			t.Log("CONTROL: AwaitDuration bounds its future wake-up by the job deadline; worker can retrieve and record timeout")
		})
	}
}
