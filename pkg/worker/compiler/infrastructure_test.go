package compiler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type unreadableProbeTask struct{ calls int }

func (*unreadableProbeTask) Name() string { return "scope_probe:run" }
func (p *unreadableProbeTask) Run(_ jobworkflow.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	p.calls++
	return jobdb.NewTaskData(map[string]any{"v": 999, "kind": "activity_output", "payload": map[string]any{}})
}

func TestInfrastructureCorruptResultBypassesRecipeCatchAndRetry(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	rt := toy.New()
	ctx := celltest.Context(context.Background())
	node := probeNode("corrupt")
	node.NodeImpl.(*recipe.NodeOp).Catch = []recipe.CatchClause{{ID: "must-not-handle", When: catchExpr(t, "true"), Continue: &recipe.CatchContinue{Outputs: map[string]any{"ok": true}}}}
	rec := probeRecipe(node, probeNode("must-not-run"))
	worker := probeWorker(t, rec)
	key := probeSubmit(t, rt, worker)
	task := &unreadableProbeTask{}
	runnable, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: []jobworkflow.TaskWorker{task, NewCellContextTaskWorker()}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	out, err := runnable.Run(nil)
	require.True(t, jobdb.IsExecutionInfrastructureError(err), "%v", err)
	var infra *jobdb.ExecutionInfrastructureError
	require.ErrorAs(t, err, &infra)
	require.Equal(t, jobdb.ExecutionInfrastructureDurableDecode, infra.Reason)
	require.NotEqual(t, jobworkflow.JobRunFailed, out.Status)
	require.Equal(t, 1, task.calls)
	info, readErr := rt.GetJob(ctx, key)
	require.NoError(t, readErr)
	require.Empty(t, info.CompletionStatus)
	require.NotEqual(t, jobdb.JobStatusCompleted, info.Status)
	// Replaying the unreadable result never invokes the callback again or writes
	// a terminal outcome. Catch/retry cannot turn it into a recipe result.
	require.True(t, jobdb.IsExecutionInfrastructureError(probeReplay(t, rt, key, worker)))
	require.Equal(t, 1, task.calls)
}

func TestInfrastructureInternalReadersAndApplicationValidation(t *testing.T) {
	for _, raw := range []string{`{`, `{"v":999,"kind":"internal_result","payload":{}}`, `{"v":2,"kind":"internal_result","payload":{"Cell":42}}`} {
		var cell jobdb.ResolvedCell
		err := coretask.DecodeInternal([]byte(raw), &cell)
		require.True(t, jobdb.IsExecutionInfrastructureError(err), "%s: %v", raw, err)
	}
	for _, raw := range []string{`{`, `{ "unexpected": true }`} {
		_, err := ParseResolvedRecipeSourceJSON([]byte(raw))
		require.True(t, jobdb.IsExecutionInfrastructureError(err))
	}
	// Valid JSON with an application output of the wrong shape remains an
	// application contract error, as do malformed authored recipe contents.
	_, err := decodeActivityInvocationOutput([]byte(`{"output":[1]}`))
	require.Error(t, err)
	require.False(t, jobdb.IsExecutionInfrastructureError(err))
	_, err = loadEmbeddedRecipeYAML([]byte("sequence: ["))
	require.Error(t, err)
	require.False(t, jobdb.IsExecutionInfrastructureError(err))
	worker := NewRecipeJobWorker(RecipeJobWorkerOptions{})
	input := &jobdb.SimpleTaskData{Data: json.RawMessage(`{"inputs":[]}`)}
	_, err = worker.Execute(&loadFailureContext{}, input)
	require.True(t, jobdb.IsExecutionInfrastructureError(err))
}

type recordedCellResultContext struct {
	jobworkflow.ExecutionContext
	cell jobdb.ResolvedCell
}

func (c recordedCellResultContext) DoTask(jobworkflow.TaskCall, func(context.Context) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	return jobdb.NewTaskData(c.cell)
}
func (recordedCellResultContext) Job() jobdb.JobHandle {
	return jobdb.JobHandle{Cell: "owner", CellID: "stable-owner", CellRepo: "example.test/owner"}
}

func TestInfrastructureRecordedCellContract(t *testing.T) {
	for _, cell := range []jobdb.ResolvedCell{
		{},
		{Cell: "owner", CellID: "different-owner", CellRepo: "example.test/owner"},
		{Cell: "owner", CellID: "stable-owner", CellRepo: "example.test/different"},
	} {
		_, err := recordedCell(recordedCellResultContext{cell: cell})
		require.True(t, jobdb.IsExecutionInfrastructureError(err), "%+v: %v", cell, err)
	}
	// The recorded name can differ after a registry rename; identity cannot.
	_, err := recordedCell(recordedCellResultContext{cell: jobdb.ResolvedCell{Cell: "old-name", CellID: "stable-owner", CellRepo: "example.test/owner"}})
	require.NoError(t, err)
}
