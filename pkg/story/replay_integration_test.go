package story_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

const replayTestOp = "story_replay_regression"

type replayFixtureTask struct {
	calls     atomic.Int32
	succeedAt int32
}

func (*replayFixtureTask) Name() string { return replayTestOp + ":" + replayTestOp }
func (w *replayFixtureTask) Run(_ jobworkflow.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	n := w.calls.Add(1)
	if n < w.succeedAt {
		return nil, &jobdb.AppError{Payload: jobdb.AppErrorPayload{Message: "intentional recorded failure"}}
	}
	envelope, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, map[string]any{"output": map[string]any{"value": "ok"}, "git": map[string]any{}, "nextTaskType": ""})
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(envelope)
}

// Shift only the reader's view of chapter times, never the stored history.
// This deterministically exercises historical job and task deadlines.
type historicalStoryRuntime struct {
	jobdb.WorkflowRuntime
	expireJob   bool
	expireTasks bool
}

func (r historicalStoryRuntime) GetChapter(ctx context.Context, ref jobdb.ChapterRef) (jobdb.Chapter, error) {
	c, err := r.WorkflowRuntime.GetChapter(ctx, ref)
	if err == nil && ((r.expireJob && ref.Ordinal == 0) || (r.expireTasks && ref.Ordinal > 0)) {
		at := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		c.CreatedAt = at
		c.Metadata.Fields["created_at"] = jobdb.ChapterMetadataValue{Kind: jobdb.ChapterMetadataString, String: at.Format(time.RFC3339Nano)}
	}
	return c, err
}

func TestStoryAPIReplaysPersistedAttemptsOrReportsIncompleteHistory(t *testing.T) {
	coreops.Register(coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: replayTestOp}, func(coreops.OpDependencies, context.Context, struct{}) (map[string]any, error) {
		panic("replay must not execute ops")
	}))
	for _, backend := range []string{"sqlite", "remote"} {
		for _, mode := range []struct {
			name      string
			succeedAt int32
			attempts  int
			failed    int
		}{
			{"success", 1, 1, 0}, {"retry then success", 9, 3, 8}, {"exhausted retries", 100, 3, 9},
		} {
			t.Run(backend+"/"+mode.name, func(t *testing.T) {
				ctx := context.Background()
				db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
				var rt jobdb.WorkflowRuntime = db
				if backend == "remote" {
					server := httptest.NewServer(remote.NewServer(db))
					t.Cleanup(server.Close)
					rt, err = remote.New(server.URL, server.Client())
					require.NoError(t, err)
				}
				worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{})
				task := &replayFixtureTask{succeedAt: mode.succeedAt}
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("story").PlusWorkers(worker, task, compiler.NewTimeoutCheckpointTaskWorker()).BuildEngine()
				require.NoError(t, err)
				recipe := "id: historical\nsequence:\n - id: outer\n   sequence:\n    - id: work\n      op: " + replayTestOp + "\n      retry: {maximum_attempts: 3, initial_interval: 1ms}\n      timeout: 5m\n"
				data, err := jobdb.NewTaskData(workflowctl.StartJob{TenantId: "story", RecipeName: "historical"}, jobdb.NewArtifactFromBytes("historical"+starter.RecipeArtifactSuffix, []byte(recipe)))
				require.NoError(t, err)
				policy := jobdb.DefaultRunPolicy()
				policy.Retry.MaximumAttempts = 3
				policy.Retry.InitialInterval = jobdb.Duration(time.Millisecond)
				key, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "story", JobType: "recipe", Data: data, RunPolicy: policy})
				require.NoError(t, err)
				runnable, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: []jobworkflow.TaskWorker{task, compiler.NewTimeoutCheckpointTaskWorker()}, WorkerID: "story-fixture", LeaseDuration: time.Minute})
				require.NoError(t, err)
				_, err = runnable.Run(nil)
				require.NoError(t, err)
				run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key})
				require.NoError(t, err)
				require.Len(t, run.Attempts, mode.attempts)
				before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
				require.NoError(t, err)
				executions := task.calls.Load()
				for _, historical := range []string{"current", "job deadline", "task deadline", "both deadlines"} {
					var replayRuntime jobdb.WorkflowRuntime = rt
					if historical != "current" {
						replayRuntime = historicalStoryRuntime{WorkflowRuntime: rt, expireJob: historical == "job deadline" || historical == "both deadlines", expireTasks: historical == "task deadline" || historical == "both deadlines"}
					}
					replayEngine, err := jobworkflow.NewEngineBuilder().WithRuntime(replayRuntime).WithWorkerTenantId("story").PlusWorkers(worker, task, compiler.NewTimeoutCheckpointTaskWorker()).BuildEngine()
					require.NoError(t, err)
					svc, err := story.New(story.ServiceConfig{Engine: replayEngine})
					require.NoError(t, err)
					st, err := svc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{ProjectID: key.TenantId, JobID: key.JobId})
					// JobDB v0.0.23 omits historical task events after expiry and the final
					// job-end event on exhausted retries. Until upstream fixes those paths,
					// the API must explicitly reject the partial reconstruction. When replay
					// succeeds, require every attempt and recorded failure in the tree.
					if errors.Is(err, story.ErrJobRunStoryIncomplete) {
						require.True(t, historical != "current" || mode.name == "exhausted retries", "healthy replay unexpectedly incomplete: %v", err)
					} else {
						require.NoError(t, err)
						require.NotNil(t, st.Root)
						require.Len(t, st.Root.PastAttempts, mode.attempts-1)
						want := story.WorkflowStatusCompleted
						if mode.name == "exhausted retries" {
							want = story.WorkflowStatusFailed
						}
						require.Equal(t, want, st.Status)
						failures := map[int64]bool{}
						var visit func(*story.JobRunStoryNode)
						visit = func(n *story.JobRunStoryNode) {
							if n == nil {
								return
							}
							if n.TaskOrdinal != nil && string(n.Status) == "failed" {
								failures[*n.TaskOrdinal] = true
							}
							for _, list := range [][]*story.JobRunStoryNode{n.Children, n.PastAttempts, n.PriorAttempts} {
								for _, child := range list {
									visit(child)
								}
							}
						}
						visit(st.Root)
						require.Len(t, failures, mode.failed)
					}
					require.Equal(t, executions, task.calls.Load(), "story reads must not re-execute tasks")
					after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
					require.NoError(t, err)
					require.Equal(t, before, after, "story reads must not alter history")
				}
			})
		}
	}
}
