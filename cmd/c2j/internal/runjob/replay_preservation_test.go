package runjob

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// This worker records a declaration from a different compiler build. The real
// c2j CLI has the same task implementation but a different declaration, so it
// must reject replay before executing the pending task.
func recordedBuildWorker() jobworkflow.ExecutionWorker {
	return jobworkflow.ExecutionWorker{JobType: "recipe", Execute: func(ctx jobworkflow.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
		_, err := workflow.ValueCall(ctx, "cell-context", compiler.CellContextTaskType, jobdb.RunPolicy{}, map[string]any{"compiler": "recorded-build"})
		return in, err
	}}
}

func TestReplayWrongWorkerPreservesAndRecovers(t *testing.T) {
	// Leave time for capability validation and cached progress replay under CI
	// load. Expiry must happen after the mismatch, not before worker admission.
	const failureLeaseDuration = 2 * time.Second
	for _, supplied := range []bool{false, true} {
		name := "ordinary"
		if supplied {
			name = "supplied"
		}
		t.Run(name, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			f := newSuppliedCLI(t, "id: preserved\nop: command_execution\ninputs: {run: 'echo must-not-run'}\n", 5*time.Second)
			capability, err := remote.DecodeLeaseCapability(f.encoded)
			require.NoError(t, err)
			initial, err := f.client.ImportLease(ctx, capability)
			require.NoError(t, err)
			seed, err := jobworkflow.GetJobForRunWithLease(ctx, f.client, initial, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: recordedBuildWorker(), ConsumerVersions: map[string]string{"c2j": "recorded-build", "c2j.commit": "recorded-commit"}})
			require.NoError(t, err)
			waiting, err := seed.Run(nil)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunSuspended, waiting.Status)
			require.NotNil(t, waiting.NextRoute)
			before, err := f.backend.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			identity, err := f.backend.GetJob(ctx, f.key)
			require.NoError(t, err)
			f.opts.LeaseDuration = failureLeaseDuration
			f.opts.OnNotReady = "fail"
			for attempt := 0; attempt < 6; attempt++ {
				f.stdout.Reset()
				f.stderr.Reset()
				if supplied {
					f.claim(t, *waiting.NextRoute, failureLeaseDuration)
					err = f.run(t, false)
				} else {
					f.denyClaims.Store(false)
					err = Run(ctx, f.opts)
				}
				require.ErrorIs(t, err, jobdb.ErrWorkflowNotDeterministic)
				var infrastructure *jobdb.ExecutionInfrastructureError
				require.ErrorAs(t, err, &infrastructure)
				var exit interface{ ExitCode() int }
				require.ErrorAs(t, err, &exit)
				require.Equal(t, 1, exit.ExitCode())
				var diagnostic executionDiagnostic
				require.NoError(t, json.Unmarshal(f.stderr.Bytes(), &diagnostic), f.stderr.String())
				require.Equal(t, f.key.JobId, diagnostic.JobID)
				require.Equal(t, jobdb.ExecutionInfrastructureDeterminism, diagnostic.Reason)
				require.Equal(t, infrastructure.Ordinal, diagnostic.Ordinal, "unknown physical ordinals must remain null")
				require.NotEmpty(t, diagnostic.Versions.C2J)
				require.NotEmpty(t, diagnostic.Versions.JobDB)
				require.NotEmpty(t, diagnostic.Writers, f.stderr.String())
				require.Contains(t, diagnostic.Writers, workerVersions{C2J: "recorded-build", Commit: stringPointer("recorded-commit"), JobDB: diagnostic.Writers[0].JobDB})
				require.NotContains(t, f.stderr.String(), "warning: replay unavailable")
				after, readErr := f.backend.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: f.key})
				require.NoError(t, readErr)
				require.Equal(t, before, after, "wrong worker must append no terminal or task result")
				// Every heartbeat owned by the invocation must stop. Real expired claims,
				// rather than changing a counter in storage, drive existing crash concern.
				want := jobdb.JobStatusReady
				if attempt == 5 {
					want = jobdb.JobStatusCrashConcern
				}
				require.Eventually(t, func() bool { info, e := f.backend.GetJob(ctx, f.key); return e == nil && info.Status == want }, 6*time.Second, 10*time.Millisecond)
				info, readErr := f.backend.GetJob(ctx, f.key)
				require.NoError(t, readErr)
				require.Equal(t, identity.CellID, info.CellID)
				require.Equal(t, identity.GoalID, info.GoalID)
				require.Equal(t, identity.WaitFor, info.WaitFor)
				require.Equal(t, identity.OtherGoalIDs, info.OtherGoalIDs)
				require.Empty(t, info.CompletionStatus)
			}
			// Operators first replace the incompatible worker, then use the existing
			// clear API. The matching compiler consumes the same pending task/history.
			require.NoError(t, f.client.ClearCrashConcern(ctx, jobdb.ClearCrashConcernRequest{JobKey: f.key, Reason: "matching worker available"}))
			f.denyClaims.Store(false)
			correct, err := jobworkflow.GetJobForRun(ctx, f.client, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: recordedBuildWorker(), TaskWorkers: []jobworkflow.TaskWorker{compiler.NewCellContextTaskWorker()}, ConsumerVersions: map[string]string{"c2j": "recorded-build", "c2j.commit": "recorded-commit"}, LeaseDuration: time.Second})
			require.NoError(t, err)
			done, err := correct.Run(nil)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunCompleted, done.Status)
			info, err := f.backend.GetJob(ctx, f.key)
			require.NoError(t, err)
			require.Equal(t, jobdb.JobStatusCompleted, info.Status)
			require.Equal(t, identity.GoalID, info.GoalID)
		})
	}
}
func stringPointer(s string) *string { return &s }

func TestInfrastructureHTTPReadPreservesAndRecovers(t *testing.T) {
	for _, supplied := range []bool{false, true} {
		name := "ordinary"
		if supplied {
			name = "supplied"
		}
		t.Run(name, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			f := newSuppliedCLI(t, "id: preserve-io\nop: command_execution\ninputs: {run: 'echo recovered'}\n", time.Second)
			capability, err := remote.DecodeLeaseCapability(f.encoded)
			require.NoError(t, err)
			initial, err := f.client.ImportLease(ctx, capability)
			require.NoError(t, err)
			seed, err := jobworkflow.GetJobForRunWithLease(ctx, f.client, initial, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{}), ConsumerVersions: map[string]string{"c2j": "recorded-build"}})
			require.NoError(t, err)
			waiting, err := seed.Run(nil)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunSuspended, waiting.Status)
			before, err := f.backend.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			f.failHistory.Store(true)
			f.opts.LeaseDuration = 250 * time.Millisecond
			f.opts.OnNotReady = "fail"
			if supplied {
				f.claim(t, *waiting.NextRoute, 250*time.Millisecond)
				err = f.run(t, false)
			} else {
				f.denyClaims.Store(false)
				err = Run(ctx, f.opts)
			}
			require.True(t, jobdb.IsExecutionInfrastructureError(err), "%v", err)
			var diagnostic executionDiagnostic
			require.NoError(t, json.Unmarshal(f.stderr.Bytes(), &diagnostic), f.stderr.String())
			require.Equal(t, jobdb.ExecutionInfrastructureRuntimeIO, diagnostic.Reason)
			require.NotContains(t, f.stderr.String(), "secret history backend response")
			after, readErr := f.backend.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, readErr)
			require.Equal(t, before, after)
			f.failHistory.Store(false)
			require.Eventually(t, func() bool {
				info, e := f.backend.GetJob(ctx, f.key)
				return e == nil && info.Status == jobdb.JobStatusReady
			}, 3*time.Second, 10*time.Millisecond)
			f.stdout.Reset()
			f.stderr.Reset()
			if supplied {
				f.claim(t, *waiting.NextRoute, time.Second)
				err = f.run(t, false)
			} else {
				err = Run(ctx, f.opts)
			}
			require.NoError(t, err, f.stderr.String())
			info, err := f.backend.GetJob(ctx, f.key)
			require.NoError(t, err)
			require.Equal(t, jobdb.JobStatusCompleted, info.Status)
		})
	}
}
