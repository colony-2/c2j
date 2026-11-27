package compiler

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func preStatusHistoryPath(t *testing.T, ctx context.Context) string {
	t.Helper()
	compressed, err := os.Open(filepath.Join("testdata", "pre-status-history", "history.db.gz"))
	require.NoError(t, err)
	defer compressed.Close()
	input, err := gzip.NewReader(compressed)
	require.NoError(t, err)
	defer input.Close()
	path := filepath.Join(t.TempDir(), "history.db")
	output, err := os.Create(path)
	require.NoError(t, err)
	_, copyErr := io.Copy(output, input)
	require.NoError(t, output.Close())
	require.NoError(t, copyErr)
	_, err = sqlite.MigrateGoals(ctx, path)
	require.NoError(t, err, "upgrade the disposable pre-status SQLite fixture")
	return path
}

func TestRecipeStatusReplaysPreFeatureHistory(t *testing.T) {
	ctx := celltest.Context(context.Background())
	runtime, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: preStatusHistoryPath(t, ctx), Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	defer runtime.Close(ctx)
	key := jobdb.JobKey{TenantId: "object-tests", JobId: "replay"}
	before, err := jobdb.GetJobRun(ctx, runtime, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true})
	require.NoError(t, err)
	info, err := runtime.GetJob(ctx, key)
	require.NoError(t, err)
	require.Equal(t, jobdbschema.PreStatusHistorySchemaHash, info.SchemaHash)
	require.NotContains(t, string(before.Start.Input.Data), "status_history_version")
	artifact, err := runtime.OpenArtifact(ctx, jobdb.ArtifactRef{JobKey: key, Ordinal: 0, Name: "status-fixture" + starter.RecipeArtifactSuffix})
	require.NoError(t, err)
	reader, err := artifact.Open()
	require.NoError(t, err)
	source, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Contains(t, string(source), "status: '${{ record_status_spy() }}'")

	spy := newStatusCELSpy(false)
	worker := NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true, CELOptionsProvider: spy})
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).WithWorkerTenantId(key.TenantId).PlusWorkers(worker).BuildEngine()
	require.NoError(t, err)
	output, err := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key})
	require.NoError(t, err)
	require.Equal(t, "unchanged", legacyBusinessOutput(t, output))
	require.Zero(t, spy.calls.Load())
	after, err := jobdb.GetJobRun(ctx, runtime, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true})
	require.NoError(t, err)
	require.Equal(t, before.Execution, after.Execution)
	require.Empty(t, statusRecordsFromRun(t, after))
	var tasks []string
	for _, entry := range after.Execution {
		if entry.Record.Kind == "task" {
			tasks = append(tasks, entry.Record.ID)
		}
	}
	require.Len(t, tasks, 2)
	require.Contains(t, tasks[0], `task:"cell-context"`)
	require.Contains(t, tasks[1], `task:"workspace:`)
}

func TestRecipeStatusRecoversPreFeatureParallelLaunch(t *testing.T) {
	ctx := celltest.Context(context.Background())
	path := preStatusHistoryPath(t, ctx)
	parent := jobdb.JobKey{TenantId: "object-tests", JobId: "interrupted"}
	list := jobdb.ListJobsRequest{TenantIds: []string{parent.TenantId}, ParentJobIDs: []string{parent.JobId}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}}
	runtime, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path, Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	children, err := runtime.ListJobs(ctx, list)
	require.NoError(t, err)
	require.Len(t, children.Jobs, 1)
	child := children.Jobs[0].JobKey
	before, err := jobdb.GetJobRun(ctx, runtime, jobdb.GetJobRunRequest{JobKey: child, IncludeInputs: true})
	require.NoError(t, err)
	info, err := runtime.GetJob(ctx, child)
	require.NoError(t, err)
	require.Equal(t, jobdbschema.PreStatusHistorySchemaHash, info.SchemaHash)
	require.NotContains(t, string(before.Start.Input.Data), "status_history_version")
	pending, err := jobdb.GetJobRun(ctx, runtime, jobdb.GetJobRunRequest{JobKey: parent, IncludeOutputs: true})
	require.NoError(t, err)
	var launchID string
	for _, entry := range pending.Execution {
		if strings.Contains(entry.Record.ID, `task:"parallel-launch:first"`) {
			require.NotEqual(t, "result", entry.Record.Kind, "old worker must not acknowledge the launch")
			if entry.Record.Kind == "task" {
				launchID = entry.Record.ID
			}
		}
	}
	require.NotEmpty(t, launchID)
	require.NoError(t, jobdbschema.Register(ctx, runtime, parent.TenantId))
	require.NoError(t, runtime.Close(ctx))

	engine, _ := objectEngineAt(t, path)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 10*time.Second, parent, engine))
	output, err := swfutil.JobResult(ctx, engine, parent)
	require.NoError(t, err)
	require.Equal(t, "original-child", legacyBusinessOutput(t, output))
	after, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: child, IncludeInputs: true})
	require.NoError(t, err)
	require.Equal(t, before.Start, after.Start)
	children, err = engine.ListJobs(ctx, list)
	require.NoError(t, err)
	require.Len(t, children.Jobs, 1)
	require.Equal(t, child, children.Jobs[0].JobKey)
	complete, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: parent, IncludeOutputs: true})
	require.NoError(t, err)
	var acknowledgements []parallelResult
	for _, entry := range complete.Execution {
		if entry.Record.Kind == "result" && strings.HasPrefix(entry.Record.ID, launchID+"/attempt:") {
			var result parallelResult
			require.NotNil(t, entry.Record.Output)
			require.NoError(t, task.DecodeInternal(entry.Record.Output.Data, &result))
			acknowledgements = append(acknowledgements, result)
		}
	}
	require.Equal(t, []parallelResult{{JobID: child.JobId, Status: "started"}}, acknowledgements)
	require.Empty(t, statusRecordsFromRun(t, complete))
}

func legacyBusinessOutput(t *testing.T, output jobdb.TaskData) string {
	t.Helper()
	var result struct{ Business string }
	require.NoError(t, json.Unmarshal(output.GetDataOrPanic(), &result))
	return result.Business
}
