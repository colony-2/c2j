package story_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/story"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// These histories were written by the unpatched worker, not created by the
// worker under test. See testdata/restart-pre-numeric/README.md for provenance.
func openPreNumericRestartFixture(t *testing.T, backend string) (*restartFixture, map[string]string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 30*time.Second)
	t.Cleanup(cancel)
	raw, err := os.ReadFile("testdata/restart-pre-numeric/jobs.sql")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "jobs.db")
	sqlDB, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = sqlDB.ExecContext(ctx, string(raw))
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
	var rt jobdb.WorkflowRuntime = db
	if backend == "remote" {
		server := httptest.NewServer(celltest.Handler(remote.NewServer(db)))
		t.Cleanup(server.Close)
		rt, err = remote.New(server.URL, server.Client())
		require.NoError(t, err)
	}
	coreops.Register(coreops.NewActivityMappedOpV2[restartProbeInput, map[string]any](coreops.OpMetadata{Type: restartProbeOp}, func(coreops.OpDependencies, context.Context, restartProbeInput) (map[string]any, error) {
		panic("replay must not execute operations")
	}))
	worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{})
	task := &restartProbeTask{}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("restart-tests").PlusWorkers(worker, task).BuildEngine()
	require.NoError(t, err)
	svc, err := story.New(story.ServiceConfig{Engine: engine, SchemaRegistry: db})
	require.NoError(t, err)
	raw, err = os.ReadFile("testdata/restart-pre-numeric/jobs.json")
	require.NoError(t, err)
	var keys map[string]string
	require.NoError(t, json.Unmarshal(raw, &keys))
	return &restartFixture{ctx: ctx, runtime: rt, engine: engine, service: svc, worker: worker, task: task}, keys
}

func assertUpgradeNumericOutput(t *testing.T, st *story.JobRunStory) {
	t.Helper()
	require.EqualValues(t, 2, st.Root.Output.(map[string]any)["count"])
	require.EqualValues(t, 1.75, st.Root.Output.(map[string]any)["fraction"])
	nested := storyNodes(st.Root, "stateMachine", "nested")
	require.Len(t, nested, 1)
	require.EqualValues(t, 1, nested[0].Output.(map[string]any)["count"])
}

func TestRestartPreFixCompletedJobCompatibility(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f, keys := openPreNumericRestartFixture(t, backend)
			f.key = jobdb.JobKey{TenantId: "restart-tests", JobId: keys["completed"]}
			original, err := f.runtime.GetJob(f.ctx, f.key)
			require.NoError(t, err)
			require.Equal(t, "success", original.CompletionStatus)
			before, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			st := f.story(t, f.key) // replay the old completed history with the new worker
			assertUpgradeNumericOutput(t, st)
			ops := storyNodes(st.Root, "op", restartProbeOp)
			require.Len(t, ops, 3)
			target := ops[2].RestartChapterOrdinal
			require.NotNil(t, target)
			inspected := f.inspect(t, f.key, *target)
			require.EqualValues(t, 0, inspected.Visible["inputs"].(map[string]any)["count"])
			require.Zero(t, f.task.calls.Load(), "reading old history must not execute tasks")
			req := story.RestartRecipeJobRequest{ProjectID: f.key.TenantId, JobID: f.key.JobId, ChapterOrdinal: target, RequestID: uuid.NewString()}
			req.Goal.Mode = "new"
			restarted, err := f.service.RestartRecipeJob(f.ctx, req)
			require.NoError(t, err)
			key := jobdb.JobKey{TenantId: restarted.TenantID, JobId: restarted.JobID}
			f.run(t, key)
			require.Equal(t, int32(1), f.task.calls.Load(), "old completed siblings remain replayed")
			assertUpgradeNumericOutput(t, f.story(t, key))
			after, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			require.Equal(t, before, after, "restart must not rewrite pre-upgrade source history")
			current, err := f.runtime.GetJob(f.ctx, f.key)
			require.NoError(t, err)
			require.Equal(t, original, current, "completed source job remains unchanged")
		})
	}
}

func TestResumePreFixInProgressJobCompatibility(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f, keys := openPreNumericRestartFixture(t, backend)
			f.key = jobdb.JobKey{TenantId: "restart-tests", JobId: keys["in_progress"]}
			job, err := f.runtime.GetJob(f.ctx, f.key)
			require.NoError(t, err)
			require.Empty(t, job.CompletionStatus, "fixture must still be unfinished at worker replacement")
			before, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			st := f.story(t, f.key)
			require.NotNil(t, st.Root)
			require.Zero(t, f.task.calls.Load(), "inspection must not finish the old job")
			// Take over the SAME job with a fresh patched worker and no resubmission.
			f.run(t, f.key)
			require.Equal(t, int32(1), f.task.calls.Load(), "both old durable task results are reused")
			assertUpgradeNumericOutput(t, f.story(t, f.key))
			after, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			require.Greater(t, len(after), len(before), "resume appends new history")
			require.Equal(t, before, after[:len(before)], "resume preserves the old history prefix")
		})
	}
}

func TestRestartPreFixInProgressJobCompatibility(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f, keys := openPreNumericRestartFixture(t, backend)
			f.key = jobdb.JobKey{TenantId: "restart-tests", JobId: keys["in_progress"]}
			original, err := f.runtime.GetJob(f.ctx, f.key)
			require.NoError(t, err)
			require.Empty(t, original.CompletionStatus)
			before, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			st := f.story(t, f.key)
			ops := storyNodes(st.Root, "op", restartProbeOp)
			require.Len(t, ops, 3, "story also renders the pending target without executing it")
			require.Zero(t, f.task.calls.Load())
			require.NotEmpty(t, ops[1].RestartScopeID)
			req := story.RestartRecipeJobRequest{ProjectID: f.key.TenantId, JobID: f.key.JobId, ScopeID: ops[1].RestartScopeID, RequestID: uuid.NewString()}
			req.Goal.Mode = "new"
			restarted, err := f.service.RestartRecipeJob(f.ctx, req)
			require.NoError(t, err)
			key := jobdb.JobKey{TenantId: restarted.TenantID, JobId: restarted.JobID}
			f.run(t, key)
			require.Equal(t, int32(2), f.task.calls.Load(), "restart reuses the old prior task and executes inner/target")
			assertUpgradeNumericOutput(t, f.story(t, key))
			after, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, err)
			require.Equal(t, before, after)
			current, err := f.runtime.GetJob(f.ctx, f.key)
			require.NoError(t, err)
			require.Equal(t, original, current, "restart leaves old unfinished source untouched")
		})
	}
}
