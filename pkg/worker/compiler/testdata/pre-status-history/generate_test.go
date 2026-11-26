package compiler

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type preStatusLaunchFault struct {
	jobworkflow.TaskWorker
	result parallelResult
}

func (fault *preStatusLaunchFault) Run(task jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	output, err := coretask.RunLocal(fault.TaskWorker, task.Context(), task, input)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(output.GetDataOrPanic(), &fault.result); err != nil {
		return nil, err
	}
	return nil, jobworkflow.EAGAINWait(task)
}

func TestGeneratePreStatusHistory(t *testing.T) {
	path := os.Getenv("C2J_GENERATE_PRE_STATUS_DB")
	if path == "" {
		t.Skip("set C2J_GENERATE_PRE_STATUS_DB to generate the fixture on the base revision")
	}
	ctx := celltest.Context(context.Background())
	runtime, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path, MaxInlineArtifactBytes: 10 << 20, Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	defer func() { require.NoError(t, runtime.Close(ctx)) }()
	start := workflowctl.StartJob{TenantId: "object-tests", Cell: "test", CellRepo: "example.test/test", RecipeName: "status-fixture"}
	celltest.Create(t, runtime, start.TenantId, start.Cell, start.CellRepo)
	require.NoError(t, jobdbschema.Register(ctx, runtime, start.TenantId))
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	guard := executionruntime.New(runtime, execution.Allocation{SchemaVersion: 1})
	var fault *preStatusLaunchFault
	workers, err := NewRecipeWorkerWithOptions(ops.NewServiceDepsBuilder().Build(), registry, RecipeJobWorkerOptions{StageExecution: guard.Stage, WrapTaskWorker: func(worker jobworkflow.TaskWorker) jobworkflow.TaskWorker {
		if worker.Name() == parallelLaunchTask {
			fault = &preStatusLaunchFault{TaskWorker: worker}
			worker = fault
		}
		return guard.WrapTaskWorker(worker)
	}})
	require.NoError(t, err)

	for _, fixture := range []struct {
		name, source string
	}{
		{"replay", `id: status-fixture
status: '${{ record_status_spy() }}'
workspace: {cell: test, repo: example.test/test, ref: main, initial: empty}
sequence:
  - id: work
    sequence: []
    outputs: {name: unchanged}
    status: '${{ record_status_spy() }}'
outputs: {business: '${{ sequence.work.outputs.name }}'}
`},
		{"interrupted", `id: status-fixture
status: '${{ record_status_spy() }}'
workspace: {cell: test, repo: example.test/test, ref: main, initial: empty}
parallel:
  branches:
    - id: first
      sequence: []
      status: '${{ record_status_spy() }}'
      outputs: {data: original-child}
outputs: {business: '${{ parallel.first.outputs.data }}'}
`},
	} {
		start.JobID = fixture.name
		parsed, err := recipe.LoadRecipeFromString([]byte(fixture.source))
		require.NoError(t, err)
		submission, err := starter.PrepareRecipeJob(ctx, start, starter.StartRecipeJobOptions{}, *parsed)
		require.NoError(t, err)
		require.NotContains(t, string(submission.Data.GetDataOrPanic()), "status_history_version")
		payload, err := submission.Data.GetData()
		require.NoError(t, err)
		submission.Data = &jobdb.SimpleTaskData{Data: payload, Artifacts: []jobdb.Artifact{jobdb.NewArtifactFromBytes(start.RecipeName+starter.RecipeArtifactSuffix, []byte(fixture.source))}}
		handle, err := runtime.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: submission})
		require.NoError(t, err)
		var tasks []jobworkflow.TaskWorker
		for _, worker := range workers.TaskWorkers {
			tasks = append(tasks, worker)
		}
		runnable, err := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: workers.JobWorker, TaskWorkers: tasks, WorkerID: "pre-status"})
		require.NoError(t, err)
		outcome, err := runnable.Run(nil)
		require.NoError(t, err)
		if fixture.name == "replay" {
			require.Equal(t, "unchanged", gitLifecycleOutput(t, outcome)["business"])
		} else {
			require.Equal(t, "started", fault.result.Status, "%v: %v; %v", outcome.Status, outcome.JobError, fault.result.Error)
			require.NotEmpty(t, fault.result.JobID)
			require.Equal(t, jobworkflow.JobRunSuspended, outcome.Status, "%#v", outcome)
		}
	}
}
