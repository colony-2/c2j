package recipejob_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type crashConcernCapture struct {
	jobdb.JobSchemaRegistry
	req   jobdb.ClearCrashConcernRequest
	calls int
	err   error
}

func (c *crashConcernCapture) ClearCrashConcern(_ context.Context, req jobdb.ClearCrashConcernRequest) error {
	c.calls++
	c.req = req
	return c.err
}

func TestClearCrashConcernValidationAndErrors(t *testing.T) {
	ctx := context.Background()
	for _, key := range []jobdb.JobKey{{}, {TenantId: "tenant", JobId: " "}, {TenantId: " ", JobId: "job"}} {
		// Validation must not need a backend.
		err := recipejob.ClearCrashConcern(ctx, nil, jobdb.ClearCrashConcernRequest{JobKey: key})
		require.ErrorIs(t, err, jobdb.ErrInvalidCrashConcernRequest)
	}
	req := jobdb.ClearCrashConcernRequest{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, WorkerID: "cortex-user", Reason: "worker repaired"}
	for _, want := range []error{nil, jobdb.ErrJobNotFound, jobdb.ErrConflict, jobdb.ErrLeasePermissionDenied, context.Canceled} {
		admin := &crashConcernCapture{err: want}
		engine := jobdbschema.WorkflowEngine{Registry: admin}
		err := recipejob.ClearCrashConcern(ctx, engine, req)
		require.ErrorIs(t, err, want)
		require.Equal(t, req, admin.req)
		require.Equal(t, 1, admin.calls)
	}
	err := (jobdbschema.WorkflowEngine{}).ClearCrashConcern(ctx, req)
	require.ErrorContains(t, err, "administration unavailable")
}

func TestClearCrashConcernRecovery(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			path := filepath.Join(t.TempDir(), "jobs.db")
			base, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, base.Close(ctx)) })
			var rt jobdb.WorkflowRuntime = base
			if backend == "http" {
				server := httptest.NewServer(celltest.Handler(remote.NewServer(base)))
				t.Cleanup(server.Close)
				rt, err = remote.New(server.URL, server.Client())
				require.NoError(t, err)
			}
			sdk, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
			require.NoError(t, err)
			engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt.(jobdb.JobSchemaRegistry)}
			for _, tenant := range []string{"tenant", "other"} {
				celltest.Create(t, rt, tenant, "test", "example.test/test")
				_, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{
					TenantId: tenant, JobID: "job", JobType: "recipe", Cell: "test", CellRepo: "example.test/test",
					Data: jobdb.NewTaskDataOrPanic("original input"), Metadata: json.RawMessage(`{"recipe":"example"}`),
					ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "reset", Value: json.RawMessage(`{"checkpoint":7}`)},
				}})
				require.NoError(t, err)
			}
			key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
			claim := jobdb.GetJobLeaseRequest{JobKey: key, WorkerID: "worker", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: time.Minute}
			lease, err := rt.GetJobLease(ctx, claim)
			require.NoError(t, err)
			require.NotNil(t, lease)
			req := jobdb.ClearCrashConcernRequest{JobKey: key, WorkerID: "operator", Reason: "repaired"}
			require.ErrorIs(t, recipejob.ClearCrashConcern(ctx, engine, req), jobdb.ErrConflict, "live leases cannot be reset")

			// Seed persisted crash counters without timing-dependent lease expiry loops.
			db, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `UPDATE jobdb_jobs SET consecutive_expirations=5, lease_expires_at_ns=0 WHERE job_id='job'`)
			require.NoError(t, err)
			require.NoError(t, db.Close())
			before, err := rt.GetJob(ctx, key)
			require.NoError(t, err)
			require.Equal(t, jobdb.JobStatusCrashConcern, before.Status)
			blocked, err := rt.GetJobLease(ctx, claim)
			require.NoError(t, err)
			require.Nil(t, blocked)
			require.NoError(t, recipejob.ClearCrashConcern(ctx, engine, req))
			require.NoError(t, recipejob.ClearCrashConcern(ctx, rt, req), "inactive retry is harmless")
			after, err := rt.GetJob(ctx, key)
			require.NoError(t, err)
			require.Equal(t, jobdb.JobStatusReady, after.Status)
			require.Equal(t, before.GoalID, after.GoalID)
			require.Equal(t, before.NextRoute, after.NextRoute)
			require.Equal(t, before.ClientPayload, after.ClientPayload)
			require.Equal(t, before.ClientPayloadRevision, after.ClientPayloadRevision)
			other, err := rt.GetJob(ctx, jobdb.JobKey{TenantId: "other", JobId: "job"})
			require.NoError(t, err)
			require.Equal(t, jobdb.JobStatusCrashConcern, other.Status)
			page, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}})
			require.NoError(t, err)
			require.Len(t, page.Jobs, 1, "clearing must not create a continuation job")
			resumed, err := rt.GetJobLease(ctx, claim)
			require.NoError(t, err)
			require.NotNil(t, resumed, "normal acquisition resumes the existing job")
			require.ErrorIs(t, recipejob.ClearCrashConcern(ctx, engine, req), jobdb.ErrConflict)
			require.NoError(t, resumed.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}))
			worker := wf.ExecutionWorker{JobType: "recipe", Execute: func(_ wf.ExecutionContext, in wf.JobData) (wf.JobData, error) { return in, nil }}
			runnable, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: key, WorkerID: "worker", JobWorker: worker, LeaseDuration: time.Minute})
			require.NoError(t, err)
			outcome, err := runnable.Run(nil)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunCompleted, outcome.Status)
			require.ErrorIs(t, recipejob.ClearCrashConcern(ctx, engine, req), jobdb.ErrConflict, "completed jobs stay completed")
			req.JobKey.JobId = "missing"
			require.ErrorIs(t, recipejob.ClearCrashConcern(ctx, engine, req), jobdb.ErrJobNotFound)
		})
	}
}
