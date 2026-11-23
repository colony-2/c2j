package jobinspect

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type workspaceObservationCounter struct {
	Runtime
	captures     int
	omitChapter  bool
	afterCapture func()
}

type workspaceIdentityWorker struct{}

func (workspaceIdentityWorker) Name() string { return "identity" }
func (workspaceIdentityWorker) Run(_ wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return in, nil
}
func (r *workspaceObservationCounter) ListChapters(ctx context.Context, q jobdb.ListChaptersRequest) ([]jobdb.Chapter, error) {
	r.captures++
	chapters, err := r.Runtime.ListChapters(ctx, q)
	if r.afterCapture != nil {
		fn := r.afterCapture
		r.afterCapture = nil
		fn()
	}
	if r.omitChapter && len(chapters) > 2 {
		chapters = append(chapters[:1], chapters[2:]...)
	}
	return chapters, err
}

func TestWorkspaceDiffObservationImmutablePrefix(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			local, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, local.Close(ctx)) })
			var rt jobdb.ConsumerRuntime = local
			if backend == "HTTP" {
				server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(local)))
				t.Cleanup(server.Close)
				rt, err = remote.New(server.URL, server.Client())
				require.NoError(t, err)
			}
			celltest.Create(t, rt, "workspace-observation", "source", "example.test/source")
			handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "workspace-observation", Cell: "source", CellRepo: "example.test/source", JobType: "observation", Data: jobdb.NewTaskDataOrPanic(map[string]any{})}})
			require.NoError(t, err)
			counter := &workspaceObservationCounter{Runtime: rt}
			service := &Service{Runtime: counter, Tenant: handle.JobKey.TenantId}
			initial, err := service.observeWorkspace(ctx, handle.JobKey.JobId)
			require.NoError(t, err)
			require.Equal(t, 1, counter.captures)
			require.Empty(t, initial.h.run.Execution)
			worker := wf.ExecutionWorker{JobType: "observation", Execute: func(c wf.ExecutionContext, in wf.JobData) (wf.JobData, error) {
				for range 270 {
					_, err := c.DoTask(wf.TaskCall{Site: "quoted/μ", TaskType: "identity", ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return jobdb.NewTaskData(map[string]any{}) })
					if err != nil {
						return nil, err
					}
				}
				return in, nil
			}}
			task := workspaceIdentityWorker{}
			run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: worker, TaskWorkers: []wf.TaskWorker{task}, WorkerID: "workspace-observation", LeaseDuration: time.Minute})
			require.NoError(t, err)
			counter.afterCapture = func() {
				out, err := run.Run(nil)
				require.NoError(t, err)
				require.Equal(t, wf.JobRunCompleted, out.Status)
			}
			concurrent, err := service.observeWorkspace(ctx, handle.JobKey.JobId)
			require.NoError(t, err)
			require.Equal(t, initial.id, concurrent.id, "append during capture must not widen its captured prefix")
			require.Empty(t, concurrent.h.run.Execution)
			require.NotEqual(t, jobdb.JobStatusCompleted, concurrent.h.run.Job.Status, "later summary status is not boundary evidence")
			before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
			require.NoError(t, err)
			require.Greater(t, len(before), 256, "cross the backend's paged chapter reads")
			observed, err := service.observeWorkspace(ctx, handle.JobKey.JobId)
			require.NoError(t, err)
			require.Equal(t, 3, counter.captures, "projection must never refresh the captured prefix")
			require.NotEqual(t, initial.id, observed.id)
			require.Empty(t, initial.h.run.Execution, "appending must not change an earlier observation")
			require.Len(t, observed.h.tasks, 270)
			for i := 1; i < len(observed.h.run.Execution); i++ {
				require.GreaterOrEqual(t, observed.h.run.Execution[i].Ordinal, observed.h.run.Execution[i-1].Ordinal)
			}
			again, err := service.observeWorkspace(ctx, handle.JobKey.JobId)
			require.NoError(t, err)
			require.Equal(t, observed.id, again.id)
			require.Equal(t, observed.h.run.Execution, again.h.run.Execution)
			after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
			require.NoError(t, err)
			require.Equal(t, before, after, "observation must not write any durable records")
			counter.omitChapter = true
			_, err = service.observeWorkspace(ctx, handle.JobKey.JobId)
			var typed *WorkspaceDiffError
			require.ErrorAs(t, err, &typed)
			require.Equal(t, "protocol", typed.Code)
		})
	}
}
