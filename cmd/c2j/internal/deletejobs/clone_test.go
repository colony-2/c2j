package deletejobs

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type sharedArtifactTask struct{ runs int }

func (t *sharedArtifactTask) Name() string { return "shared-artifact" }
func (t *sharedArtifactTask) Run(_ wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	t.runs++
	return in, nil
}

func TestDeletionPreservesCompletedOutcomeAndCloneArtifacts(t *testing.T) {
	for _, backend := range []string{"embed", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			f := newDeletionFixture(t, backend)
			h := f.open(t)
			task := &sharedArtifactTask{}
			worker := wf.ExecutionWorker{JobType: "clone-test", Execute: func(c wf.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
				out, err := c.DoTask(wf.TaskCall{Site: "shared", TaskType: task.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return in, nil })
				if err != nil {
					return nil, err
				}
				return c.DoScope("restart", wf.ScopeOptions{OnRestart: func(json.RawMessage) error { return nil }}, func(wf.ExecutionContext) (jobdb.JobData, error) { return out, nil })
			}}
			run := func(rt jobdb.WorkflowRuntime, key jobdb.JobKey) {
				r, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: []wf.TaskWorker{task}, WorkerID: "test", LeaseDuration: time.Hour})
				require.NoError(t, err)
				outcome, err := r.Run(nil)
				require.NoError(t, err)
				require.Equal(t, wf.JobRunCompleted, outcome.Status)
				require.NoError(t, outcome.JobError)
			}
			source, err := h.Runtime.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{
				TenantId: f.tenant, JobID: "source", JobType: worker.Name(),
				Data: &jobdb.SimpleTaskData{Data: json.RawMessage(`"output"`), Artifacts: []jobdb.Artifact{jobdb.NewArtifactFromBytes("shared.txt", []byte("shared contents"))}},
			}})
			require.NoError(t, err)
			run(h.Runtime, source.JobKey)
			history, err := jobdb.GetJobRun(ctx, h.Runtime, jobdb.GetJobRunRequest{JobKey: source.JobKey})
			require.NoError(t, err)
			var scopeID string
			for _, entry := range history.Execution {
				if entry.Record.Kind == "scope" && strings.HasSuffix(entry.Record.ID, `scope:"restart"#1`) {
					scopeID = entry.Record.ID
				}
			}
			require.NotEmpty(t, scopeID)
			clone, err := h.Runtime.SubmitRestartJob(ctx, jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{
				PriorJobKey: source.JobKey, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: scopeID},
			}})
			require.NoError(t, err)
			require.NoError(t, h.Cleanup())
			require.EqualValues(t, 1, f.run(t, Options{JobIDs: []string{source.JobKey.JobId}}).DeletedCount)
			h = f.open(t)
			defer h.Cleanup()
			info, err := h.Runtime.GetJob(ctx, source.JobKey)
			require.NoError(t, err)
			require.Equal(t, jobdb.JobStatusCompleted, info.Status)
			run(h.Runtime, clone.JobKey)
			engine, err := wf.NewEngineBuilder().WithRuntime(h.Runtime).WithWorkerTenantId(f.tenant).PlusWorkers(worker, task).BuildEngine()
			require.NoError(t, err)
			out, err := engine.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: clone.JobKey})
			require.NoError(t, err)
			artifacts, err := out.GetArtifacts()
			require.NoError(t, err)
			require.Len(t, artifacts, 1)
			reader, err := artifacts[0].Open()
			require.NoError(t, err)
			contents, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.NoError(t, reader.Close())
			require.Equal(t, "shared contents", string(contents))
			require.Equal(t, 1, task.runs, "clone reuses shared work after deleting its source")
		})
	}
}
