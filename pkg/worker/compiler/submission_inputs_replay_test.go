package compiler

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRecipeSubmissionInputsHistoricalReplayAndRestart(t *testing.T) {
	for _, name := range []string{"declared-success", "undeclared-failure"} {
		t.Run(name, func(t *testing.T) {
			sentinel := submissionInputsSentinel(t)
			path := filepath.Join(t.TempDir(), "jobs.db")
			src, err := os.Open(filepath.Join("testdata/recipe-submission-inputs", name+".db.gz"))
			require.NoError(t, err)
			defer src.Close()
			unzip, err := gzip.NewReader(src)
			require.NoError(t, err)
			defer unzip.Close()
			dst, err := os.Create(path)
			require.NoError(t, err)
			_, err = io.Copy(dst, unzip)
			require.NoError(t, err)
			require.NoError(t, dst.Close())
			raw, err := os.ReadFile(filepath.Join("testdata/recipe-submission-inputs", name+".json"))
			require.NoError(t, err)
			var expected submissionInputsFixture
			require.NoError(t, json.Unmarshal(raw, &expected))
			ctx := celltest.Context(context.Background())
			// Upgrade only the disposable legacy copy; normal opens stay strict.
			plan, err := sqlite.CheckGoalMigration(ctx, path)
			require.NoError(t, err)
			require.Contains(t, []string{"CONVERTIBLE", "CONVERTIBLE_WITH_NOTICES"}, plan.Verdict)
			migrated, err := sqlite.MigrateGoals(ctx, path)
			require.NoError(t, err)
			require.Equal(t, "MIGRATED", migrated.Verdict)
			rt, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path, MaxInlineArtifactBytes: 10 << 20, Projects: jobdbcelltest.UnrestrictedProjects{}})
			require.NoError(t, err)
			defer rt.Close(ctx)
			before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: expected.Key})
			require.NoError(t, err)
			info, err := rt.GetJob(ctx, expected.Key)
			require.NoError(t, err)
			worker := NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(expected.Key.TenantId).PlusWorkers(worker).BuildEngine()
			require.NoError(t, err)
			// No task workers or recipe sources are available to this replay engine.
			replay, err := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: expected.Key})
			if name == "declared-success" {
				require.NoError(t, err)
				var output map[string]any
				require.NoError(t, json.Unmarshal(replay.GetDataOrPanic(), &output))
				require.Equal(t, expected.Output, output)
			} else {
				// The old root failure is recorded before any recipe scope exists.
				// JobDB detects that absent scope before reaching an unrecorded task.
				require.ErrorIs(t, err, jobworkflow.ErrWorkflowNotDeterministic)
			}
			require.Zero(t, sentinel.calls.Load())
			after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: expected.Key})
			require.NoError(t, err)
			require.Equal(t, before, after)
			afterInfo, err := rt.GetJob(ctx, expected.Key)
			require.NoError(t, err)
			require.Equal(t, info, afterInfo)
			if name == "undeclared-failure" {
				restarted, err := rt.SubmitRestartJob(ctx, jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: expected.Key, Cell: "test", LastStepToKeep: 0}})
				require.NoError(t, err)
				retained, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: restarted.JobKey})
				require.NoError(t, err)
				require.Equal(t, before[:1], retained, "restart retains the complete original submission")
				out := probeRun(t, rt, restarted.JobKey, NewRecipeJobWorker(RecipeJobWorkerOptions{}), sentinel)
				got := gitLifecycleOutput(t, out)
				require.Equal(t, map[string]any{}, got["all"])
				require.Equal(t, true, got["called"])
				require.EqualValues(t, 1, sentinel.calls.Load())
				require.NoError(t, probeReplay(t, rt, restarted.JobKey, worker))
				require.EqualValues(t, 1, sentinel.calls.Load())
				after, err = rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: expected.Key})
				require.NoError(t, err)
				require.Equal(t, before, after)
				afterInfo, err = rt.GetJob(ctx, expected.Key)
				require.NoError(t, err)
				require.Equal(t, info, afterInfo, "original failure is unchanged")
			}
		})
	}
}

func TestRecipeSubmissionInputsCandidateReplay(t *testing.T) {
	sentinel := submissionInputsSentinel(t)
	rt, err := sqlite.NewFromConfig(testBackground(), sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	defer rt.Close(testBackground())
	key := submitSubmissionInputs(t, rt, false)
	out := probeRun(t, rt, key, NewRecipeJobWorker(RecipeJobWorkerOptions{}), sentinel)
	expected := gitLifecycleOutput(t, out)
	require.Equal(t, map[string]any{}, expected["all"])
	before, err := rt.ListChapters(celltest.Context(testBackground()), jobdb.ListChaptersRequest{JobKey: key})
	require.NoError(t, err)
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
	require.NoError(t, err)
	replay, err := engine.ReplayJobRun(celltest.Context(testBackground()), jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
	require.NoError(t, err)
	require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(replay.GetDataOrPanic()))
	after, err := rt.ListChapters(celltest.Context(testBackground()), jobdb.ListChaptersRequest{JobKey: key})
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.EqualValues(t, 1, sentinel.calls.Load())
}
