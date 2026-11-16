package jobinspect_test

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestChangesJ20J21J31J40ForeignLineageAndRestart(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			source := changesFixture(t, ctx, rt, changestest.Options{})
			api := changesAPI(t, rt, jobinspect.ChangesConfig{})
			h, e := api.History(ctx, jobinspect.ChangesRequest{JobID: source.Job.JobId})
			require.NoError(t, e)
			var primary jobdb.ArtifactKey
			for _, s := range h.Sources {
				if s.Producer.Task == h.State.Task {
					primary = s.Key
				}
			}
			require.NotEmpty(t, primary.JobId)
			state := source.Steps[len(source.Steps)-1].State
			child := changesFixture(t, ctx, rt, changestest.Options{RestoreInput: func(int) *jobdb.ArtifactKey { return &primary }, Transform: func(_ int, s *changestest.Step) { s.State = state; s.Bundle = nil }})
			// Delete both producing checkouts. Only the authorized fixed incoming edge
			// and its earlier source-job prerequisites may reconstruct this workspace.
			require.NoError(t, source.DeleteCheckout())
			require.NoError(t, child.DeleteCheckout())
			beforeSource, e := changestest.Inventory(ctx, rt, source.Job)
			require.NoError(t, e)
			beforeChild, e := changestest.Inventory(ctx, rt, child.Job)
			require.NoError(t, e)
			readOnly := &changestest.ReadOnlyRuntime{WorkflowRuntime: rt}
			api = changesAPI(t, readOnly, jobinspect.ChangesConfig{})
			ch, e := api.History(ctx, jobinspect.ChangesRequest{JobID: child.Job.JobId})
			require.NoError(t, e)
			require.Equal(t, source.Head, ch.Head)
			require.Equal(t, child.Job.JobId, ch.State.JobID)
			require.Len(t, ch.Sources, 2)
			for _, s := range ch.Sources {
				require.Equal(t, source.Job.JobId, s.Producer.JobID)
			}
			_, _ = readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: child.Job.JobId}, Commit: source.Commits[1]})
			denied, e := jobinspect.NewChangesService(&jobinspect.Service{Runtime: readOnly, Tenant: "tenant", Authorize: func(_ context.Context, id string) error {
				if id == source.Job.JobId {
					return jobdb.ErrCellForbidden
				}
				return nil
			}}, jobinspect.ChangesConfig{})
			require.NoError(t, e)
			_, e = denied.History(ctx, jobinspect.ChangesRequest{JobID: child.Job.JobId})
			changesCode(t, e, "job_denied")
			// Native replay executes the recorded orchestration without writer calls.
			engine, e := wf.NewEngineBuilder().WithRuntime(readOnly).WithWorkerTenantId("tenant").PlusWorkers(source.Worker).BuildEngine()
			require.NoError(t, e)
			_, e = engine.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: source.Job, JobWorker: source.Worker})
			require.NoError(t, e)
			afterSource, e := changestest.Inventory(ctx, rt, source.Job)
			require.NoError(t, e)
			afterChild, e := changestest.Inventory(ctx, rt, child.Job)
			require.NoError(t, e)
			require.Equal(t, string(beforeSource), string(afterSource))
			require.Equal(t, string(beforeChild), string(afterChild))
			require.Zero(t, readOnly.Mutations.Load())
			// A restart initially displays its retained prefix, with original source
			// keys. Running it then records a newer main state in the restarted job.
			log, e := (&jobinspect.Service{Runtime: rt, Tenant: "tenant"}).Inspect(ctx, jobinspect.Request{Operation: "log", JobID: source.Job.JobId})
			require.NoError(t, e)
			require.Len(t, log.Tasks, 4)
			restarted, e := rt.SubmitRestartJob(ctx, jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{Cell: "changes", PriorJobKey: source.Job, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: log.Tasks[3].ID}}})
			require.NoError(t, e)
			rh, e := api.History(ctx, jobinspect.ChangesRequest{JobID: restarted.JobKey.JobId})
			require.NoError(t, e)
			require.Equal(t, source.Commits[1], rh.Head)
			require.NoError(t, source.RunRecorded(ctx, rt, restarted.JobKey))
			fresh, e := api.History(ctx, jobinspect.ChangesRequest{JobID: restarted.JobKey.JobId})
			require.NoError(t, e)
			require.Equal(t, source.Head, fresh.Head)
			_, e = api.History(ctx, jobinspect.ChangesRequest{JobID: restarted.JobKey.JobId, Selection: rh.Selection})
			changesCode(t, e, "stale_selection")
		})
	}
}
