package recipejob_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/joblist"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestExplicitTitleStrictChildCompletionAndReplay(t *testing.T) {
	for _, backend := range []string{"embedded", "remote"} {
		t.Run(backend, func(t *testing.T) {
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
			sdk, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("t").PlusWorkers(compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
			require.NoError(t, err)
			engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt.(jobdb.JobSchemaRegistry)}
			rec, err := recipe.LoadRecipeFromString([]byte(`id: strict
input_schema:
  value: {type: string, required: true}
inputs: {value: '${{ inputs.value }}'}
sequence: []
outputs:
  value: '${{ inputs.value }}'
`))
			require.NoError(t, err)
			registry, err := workerops.NewActivityRegistry()
			require.NoError(t, err)
			guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
			workers, err := compiler.NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().Build(), registry, compiler.RecipeJobWorkerOptions{Allocation: guard.Allocation, StageExecution: guard.Stage, WrapTaskWorker: guard.WrapTaskWorker})
			require.NoError(t, err)
			var tasks []wf.TaskWorker
			for _, w := range workers.TaskWorkers {
				tasks = append(tasks, w)
			}
			for _, tc := range []struct{ id, title, want string }{
				{"explicit", "  Café\nUI  ", "  Café\nUI  "},
				{"unsafe", "${{ context.secrets.private }}", ""},
				{"fallback-template", "${{ context.secrets.?private.orValue(\"public\") }}", ""},
			} {
				t.Run(tc.id, func(t *testing.T) {
					start := workflowctl.StartJob{TenantId: "t", JobID: tc.id, Cell: "test", CellRepo: celltest.Repo(""), RecipeName: "strict", Title: tc.title, Inputs: map[string]any{"value": "original"}}
					celltest.Create(t, rt.(jobdb.JobSchemaRegistry), "t", start.Cell, start.CellRepo)
					ref, err := starter.StartRecipeJob(ctx, start, engine, *rec)
					require.NoError(t, err)
					job, err := starter.PrepareRecipeJob(ctx, start, starter.StartRecipeJobOptions{}, *rec)
					require.NoError(t, err)
					// Inspect the prepared input payload independently of the recipe result.
					raw, err := job.Data.GetData()
					require.NoError(t, err)
					var persisted workflowctl.StartJob
					require.NoError(t, json.Unmarshal(raw, &persisted))
					require.Equal(t, start.Inputs, persisted.Inputs)
					require.Equal(t, start.Title, persisted.Title)
					runnable, err := wf.GetJobForRun(ctx, guard, wf.GetJobForRunRequest{JobKey: ref.Key(), JobWorker: workers.JobWorker, TaskWorkers: tasks, WorkerID: "titles", LeaseDuration: time.Minute})
					require.NoError(t, err)
					out, err := runnable.Run(nil)
					require.NoError(t, err)
					require.Equal(t, wf.JobRunCompleted, out.Status, "%+v", out)
					require.JSONEq(t, `{"value":"original"}`, string(out.Output.GetDataOrPanic()))
					replayed, err := sdk.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: ref.Key(), JobWorker: compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})})
					require.NoError(t, err)
					require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
					page, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"t"}, JobKeys: []jobdb.JobKey{ref.Key()}})
					require.NoError(t, err)
					require.Len(t, page.Jobs, 1)
					var meta starter.JobMetadata
					require.NoError(t, json.Unmarshal(page.Jobs[0].Metadata, &meta))
					require.Equal(t, tc.want, meta.Title)
					require.Equal(t, tc.want, joblist.JobFromSummary(page.Jobs[0]).Title)
					listed, err := recipejob.ListRecipeJobs(ctx, &completionListOnly{list: rt.ListJobs}, recipejob.ListRecipeJobsRequest{TenantID: "t", JobIDs: []string{ref.JobID}})
					require.NoError(t, err)
					require.Len(t, listed.Jobs, 1)
					require.Equal(t, tc.want, listed.Jobs[0].Title)
					require.Equal(t, map[string]any{"value": "original"}, start.Inputs)
				})
			}
			// A title cannot make undeclared inputs legal.
			bad := workflowctl.StartJob{TenantId: "t", JobID: "invalid", Cell: "test", CellRepo: celltest.Repo(""), RecipeName: "strict", Title: "valid", Inputs: map[string]any{"value": "original", "extra": "invalid"}}
			ref, err := starter.StartRecipeJob(ctx, bad, engine, *rec)
			require.NoError(t, err)
			runnable, err := wf.GetJobForRun(ctx, guard, wf.GetJobForRunRequest{JobKey: ref.Key(), JobWorker: workers.JobWorker, TaskWorkers: tasks, WorkerID: "titles", LeaseDuration: time.Minute})
			require.NoError(t, err)
			out, runErr := runnable.Run(nil)
			require.NoError(t, runErr)
			require.Equal(t, wf.JobRunFailed, out.Status)
			require.ErrorContains(t, out.JobError, "extra")
		})
	}
}
