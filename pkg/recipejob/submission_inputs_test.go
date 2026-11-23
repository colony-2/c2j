package recipejob_test

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRecipeSubmissionInputsRecordingAndListings(t *testing.T) {
	for _, backend := range []string{"embedded", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
			var rt jobdb.ConsumerRuntime = db
			if backend == "remote" {
				server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(db)))
				t.Cleanup(server.Close)
				rt, err = remote.New(server.URL, server.Client())
				require.NoError(t, err)
			}
			sdk, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("t").PlusWorkers(compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
			require.NoError(t, err)
			engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt.(jobdb.JobSchemaRegistry)}
			registry, err := workerops.NewActivityRegistry()
			require.NoError(t, err)
			guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
			workers, err := compiler.NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().Build(), registry, compiler.RecipeJobWorkerOptions{Allocation: guard.Allocation, StageExecution: guard.Stage, WrapTaskWorker: guard.WrapTaskWorker})
			require.NoError(t, err)
			var tasks []wf.TaskWorker
			for _, w := range workers.TaskWorkers {
				tasks = append(tasks, w)
			}
			for index, tc := range []struct {
				name     string
				value    any
				want     string
				defaults bool
			}{
				{name: "text", value: "  Café\n日本語  ", want: "  Café\n日本語  "},
				{name: "empty", value: ""}, {name: "blank", value: " \n "}, {name: "number", value: 42},
				{name: "null", value: nil}, {name: "object", value: map[string]any{"text": "opaque"}},
				{name: "secret", value: secrets.Reference{Name: "unresolved"}},
				{name: "secret-template", value: "${{ context.secrets.unresolved }}"},
				{name: "defaults", defaults: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					raw := map[string]any{"value": "run"}
					want := map[string]any{"value": "run"}
					schema := "input_schema:\n  value: {type: string, required: true}\n"
					if tc.defaults {
						schema += "  title: {type: string, default_value: DefaultTitle}\n  prompt: {type: string, default_value: DefaultPrompt}\n"
						want["title"] = "DefaultTitle"
						want["prompt"] = "DefaultPrompt"
					} else {
						raw["title"] = tc.value
						raw["prompt"] = tc.value
					}
					rec, err := recipe.LoadRecipeFromString([]byte("id: submission-inputs\n" + schema + "inputs: {all: '${{ inputs }}'}\nsequence: []\noutputs: {all: '${{ inputs.all }}'}\n"))
					require.NoError(t, err)
					start, err := recipejob.BuildStartJob(recipejob.BuildStartJobRequest{TenantID: "t", JobID: fmt.Sprint(index), Recipe: "submission-inputs", Inputs: raw, Target: recipejob.ResolvedTarget{CellName: "test", RepositorySource: celltest.Repo("")}})
					require.NoError(t, err)
					fullHash := recipejob.InputHash(raw)
					require.Equal(t, fullHash, start.InputHash)
					celltest.Create(t, engine, "t", "test", start.CellRepo)
					ref, err := starter.StartRecipeJob(ctx, start, engine, *rec)
					require.NoError(t, err)
					runnable, err := wf.GetJobForRun(ctx, guard, wf.GetJobForRunRequest{JobKey: ref.Key(), JobWorker: workers.JobWorker, TaskWorkers: tasks, WorkerID: "submission-inputs", LeaseDuration: time.Minute})
					require.NoError(t, err)
					out, err := runnable.Run(nil)
					require.NoError(t, err)
					require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
					var output map[string]any
					require.NoError(t, json.Unmarshal(out.Output.GetDataOrPanic(), &output))
					require.Equal(t, want, output["all"])
					chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: ref.Key()})
					require.NoError(t, err)
					first, ok := chapters[0].Body.(jobdb.JobStartChapter)
					require.True(t, ok)
					var persisted workflowctl.StartJob
					require.NoError(t, json.Unmarshal(first.Input.Data, &persisted))
					rawJSON, err := json.Marshal(raw)
					require.NoError(t, err)
					persistedJSON, err := json.Marshal(persisted.Inputs)
					require.NoError(t, err)
					require.JSONEq(t, string(rawJSON), string(persistedJSON))
					require.Equal(t, fullHash, persisted.InputHash)
					if !tc.defaults {
						require.NotEqual(t, recipejob.InputHash(want), fullHash)
					}
					page, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"t"}, JobKeys: []jobdb.JobKey{ref.Key()}})
					require.NoError(t, err)
					require.Len(t, page.Jobs, 1)
					var meta starter.JobMetadata
					require.NoError(t, json.Unmarshal(page.Jobs[0].Metadata, &meta))
					require.Equal(t, tc.want, meta.Title)
					require.Equal(t, tc.want, meta.Prompt)
					require.Equal(t, fullHash, meta.InputHash)
					listed := joblist.JobFromSummary(page.Jobs[0])
					require.Equal(t, tc.want, listed.Title)
					require.Equal(t, tc.want, listed.Prompt)
					recipes, err := recipejob.ListRecipeJobs(ctx, rt, recipejob.ListRecipeJobsRequest{TenantID: "t", JobIDs: []string{ref.JobID}})
					require.NoError(t, err)
					require.Len(t, recipes.Jobs, 1)
					require.Equal(t, tc.want, recipes.Jobs[0].Title)
					require.Equal(t, tc.want, recipes.Jobs[0].Prompt)
					replay, err := sdk.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: ref.Key(), JobWorker: compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})})
					require.NoError(t, err)
					require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(replay.GetDataOrPanic()))
				})
			}
		})
	}
}
