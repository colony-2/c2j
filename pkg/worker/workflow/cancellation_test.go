package workflow

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestInspectCancellationOverridesRetainedAttemptError(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, reason := range []string{"", "  Operator feedback\n请使用现有附件 ☃\n "} {
			t.Run(backend+"/"+reason, func(t *testing.T) {
				ctx := celltest.Context(context.Background())
				db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
				var rt jobdb.WorkflowRuntime = db
				if backend == "remote" {
					server := httptest.NewServer(celltest.Handler(remote.NewServer(db)))
					t.Cleanup(server.Close)
					rt, err = remote.New(server.URL, server.Client())
					require.NoError(t, err)
				}
				celltest.Create(t, rt, "tenant", "test", "example.test/test")
				h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: "producer", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
				require.NoError(t, err)
				lease, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: h.JobKey, WorkerID: "test", Routes: []jobdb.Route{{JobType: "producer"}}, LeaseDuration: time.Minute})
				require.NoError(t, err)
				require.NotNil(t, lease)
				chapter := jobdb.Chapter{Ordinal: 1, TaskType: "producer", CreatedAt: time.Now().UTC(), Body: jobdb.JobAttemptOutcomeChapter{Outcome: jobdb.AppErrorOutcome{Error: jobdb.AppErrorPayload{Message: "earlier attempt failure"}}}}
				token := ""
				if remoteLease, ok := lease.(interface{ LeaseToken() string }); ok {
					token = remoteLease.LeaseToken()
				}
				require.NoError(t, rt.PutChapter(ctx, jobdb.PutChapterRequest{Ref: jobdb.ChapterRef{JobKey: h.JobKey, Ordinal: 1}, LeaseID: lease.LeaseID(), LeaseToken: token, Chapter: chapter}))
				require.NoError(t, lease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "producer"}}))
				require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: h.JobKey, Reason: reason}))
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, err)
				run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: h.JobKey})
				require.NoError(t, err)
				require.NotEmpty(t, run.Attempts)
				require.NotNil(t, latestInspectionAttempt(run.Attempts).Outcome.Error)
				got, err := (&SWFWorkflowControl{Engine: engine}).InspectJob(ctx, h.JobKey)
				require.NoError(t, err)
				require.True(t, got.Terminal)
				require.Equal(t, "cancelled", got.Status)
				require.Equal(t, "cancellation", got.FailureKind)
				require.Equal(t, reason, got.FailureMessage)
			})
		}
	}
}
