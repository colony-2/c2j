package story_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestInboxPublicRetainedRecords(t *testing.T) {
	const op = "inbox_recorded_probe"
	coreops.Register(coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: op, AcceptsArtifacts: true}, func(coreops.OpDependencies, context.Context, struct{}) (map[string]any, error) {
		panic("inspection must not execute")
	}))
	for _, backend := range []string{"sqlite", "remote"} {
		for _, size := range []int{12, 140000} {
			for _, succeedAt := range []int32{1, 4} {
				t.Run(fmt.Sprintf("%s/%d/success-at-%d", backend, size, succeedAt), func(t *testing.T) {
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
					worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{})
					task := &replayFixtureTask{succeedAt: succeedAt}
					// Reuse the fixture outcome, while retaining the new op's invocation.
					proxy := inboxFixtureTask{task: task, name: op + ":" + op}
					engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("story").PlusWorkers(worker, proxy).BuildEngine()
					require.NoError(t, err)
					content := strings.Repeat("x", size)
					recipe := "id: inbox\nsequence:\n - id: work\n   op: " + op + "\n   retry: {maximum_attempts: 2, initial_interval: 1ms}\n   artifacts:\n     docs/message.txt:\n       kind: value\n       format: text\n       value: '" + content + "'\n"
					start, err := jobdb.NewTaskData(workflowctl.StartJob{TenantId: "story", RecipeName: "inbox", Cell: "test", CellRepo: celltest.Repo("")}, jobdb.NewArtifactFromBytes("inbox"+starter.RecipeArtifactSuffix, []byte(recipe)))
					require.NoError(t, err)
					celltest.Create(t, rt, "story", "test", "example.test/test")
					policy := jobdb.DefaultRunPolicy()
					policy.Retry.MaximumAttempts = 2
					policy.Retry.InitialInterval = jobdb.Duration(time.Millisecond)
					handle, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "story", JobType: "recipe", Data: start, RunPolicy: policy, Cell: "test", CellRepo: "example.test/test"})
					require.NoError(t, err)
					runnable, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: worker, TaskWorkers: []jobworkflow.TaskWorker{compiler.NewCellContextTaskWorker(), proxy}, WorkerID: "inbox-fixture", LeaseDuration: time.Minute})
					require.NoError(t, err)
					_, err = runnable.Run(nil)
					require.NoError(t, err)
					svc, err := story.New(story.ServiceConfig{Engine: engine})
					require.NoError(t, err)
					before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
					require.NoError(t, err)
					st, err := svc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{ProjectID: "story", JobID: handle.JobKey.JobId})
					require.NoError(t, err)
					var found *story.JobRunStoryNode
					var visit func(*story.JobRunStoryNode)
					visit = func(n *story.JobRunStoryNode) {
						if n == nil {
							return
						}
						if n.Inbox != nil && len(n.Inbox.Entries) > 0 {
							found = n
						}
						for _, ch := range n.Children {
							visit(ch)
						}
					}
					visit(st.Root)
					if found == nil {
						raw, _ := json.Marshal(st)
						t.Fatalf("missing inbox; calls=%d story=%s", task.calls.Load(), raw)
					}
					require.NotNil(t, found)
					require.Equal(t, "available", found.Inbox.Status)
					require.Len(t, found.Inbox.Entries, 1)
					require.Equal(t, "value", found.Inbox.Entries[0].Content)
					inventory, err := json.Marshal(found.Inbox)
					require.NoError(t, err)
					require.NotContains(t, string(inventory), content)
					req := story.GetInboxValueRequest{ProjectID: "story", JobID: handle.JobKey.JobId, JobAttempt: st.Root.JobAttempt, InvocationID: found.InvocationID, TaskOrdinal: *found.TaskOrdinal, TaskAttempt: found.Attempt, Binding: "docs/message.txt"}
					value, err := svc.GetInboxValue(ctx, req)
					require.NoError(t, err)
					require.Equal(t, content, string(value.Data))
					require.Equal(t, "docs/message.txt", value.Filename)
					req.Binding = "missing"
					_, err = svc.GetInboxValue(ctx, req)
					require.EqualError(t, err, "inbox: binding_unknown")
					req.TaskAttempt += 100
					_, err = svc.GetInboxValue(ctx, req)
					require.EqualError(t, err, "inbox: history_missing")
					require.Equal(t, succeedAt, task.calls.Load())

					// Every prior task and job attempt must retain an independently
					// addressable selection, including compact shared ordinals.
					roots := append([]*story.JobRunStoryNode{st.Root}, st.Root.PastAttempts...)
					for _, root := range roots {
						var inspect func(*story.JobRunStoryNode)
						inspect = func(n *story.JobRunStoryNode) {
							if n == nil {
								return
							}
							if n.Inbox != nil && len(n.Inbox.Entries) > 0 {
								got, err := svc.GetInboxValue(ctx, story.GetInboxValueRequest{ProjectID: "story", JobID: handle.JobKey.JobId, JobAttempt: root.JobAttempt, InvocationID: n.InvocationID, TaskOrdinal: *n.TaskOrdinal, TaskAttempt: n.Attempt, Binding: "docs/message.txt"})
								require.NoError(t, err)
								require.Equal(t, content, string(got.Data))
							}
							for _, nodes := range [][]*story.JobRunStoryNode{n.Children, n.PriorAttempts} {
								for _, child := range nodes {
									inspect(child)
								}
							}
						}
						inspect(root)
					}
					if size > 128*1024 && succeedAt == 1 {
						for _, denial := range []struct {
							err    error
							reason string
						}{{fs.ErrPermission, "attachment_denied"}, {fs.ErrNotExist, "attachment_missing"}} {
							deniedEngine, err := jobworkflow.NewEngineBuilder().WithRuntime(inboxDeniedRuntime{WorkflowRuntime: rt, err: denial.err}).WithWorkerTenantId("story").PlusWorkers(worker, proxy).BuildEngine()
							require.NoError(t, err)
							deniedSvc, err := story.New(story.ServiceConfig{Engine: deniedEngine})
							require.NoError(t, err)
							deniedStory, err := deniedSvc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{ProjectID: "story", JobID: handle.JobKey.JobId})
							require.NoError(t, err)
							var check func(*story.JobRunStoryNode)
							check = func(n *story.JobRunStoryNode) {
								if n.TaskOrdinal != nil && n.OpID == op {
									require.Equal(t, denial.reason, n.Inbox.Reason)
									require.Empty(t, n.Inbox.Entries)
								}
								for _, child := range n.Children {
									check(child)
								}
							}
							check(deniedStory.Root)
							deniedReq := story.GetInboxValueRequest{ProjectID: "story", JobID: handle.JobKey.JobId, JobAttempt: st.Root.JobAttempt, InvocationID: found.InvocationID, TaskOrdinal: *found.TaskOrdinal, TaskAttempt: found.Attempt, Binding: "docs/message.txt"}
							_, err = deniedSvc.GetInboxValue(ctx, deniedReq)
							require.EqualError(t, err, "inbox: "+denial.reason)
						}
					}
					after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
					require.NoError(t, err)
					require.Equal(t, before, after)
				})
			}
		}
	}
}

type inboxFixtureTask struct {
	task *replayFixtureTask
	name string
}

func (p inboxFixtureTask) Name() string { return p.name }
func (p inboxFixtureTask) Run(ctx jobworkflow.TaskContext, td jobdb.TaskData) (jobdb.TaskData, error) {
	return p.task.Run(ctx, td)
}

type inboxDeniedRuntime struct {
	jobdb.WorkflowRuntime
	err error
}

func (r inboxDeniedRuntime) OpenArtifact(ctx context.Context, ref jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	if strings.Contains(ref.Name, "c2j-invocation-input.json") {
		return nil, r.err
	}
	return r.WorkflowRuntime.OpenArtifact(ctx, ref)
}
