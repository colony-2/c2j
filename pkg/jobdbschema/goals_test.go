package jobdbschema

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type submissionNoReads struct {
	jobdb.WorkflowRuntime
	jobdb.JobSchemaRegistry
	reads int
}

func (r *submissionNoReads) GetJob(context.Context, jobdb.JobKey) (jobdb.JobInfo, error) {
	r.reads++
	panic("submission must preserve the response without identity lookup")
}
func TestSubmitReferencePreservesIdentityWithoutRead(t *testing.T) {
	raw := toy.New()
	rt := &submissionNoReads{WorkflowRuntime: raw, JobSchemaRegistry: raw}
	sdk, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	engine := WorkflowEngine{Engine: sdk, Registry: rt}
	request := jobdb.SubmitJob{TenantId: "t", JobID: "stable-child", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(map[string]any{"cell": "test", "tenantId": "t", "recipe": "test", "context": map[string]any{}}), Cell: "test", CellRepo: "example.test/test"}
	celltest.Create(t, engine, request.TenantId, request.Cell, request.CellRepo)
	first, err := engine.SubmitJobRef(celltest.Context(context.Background()), request)
	require.NoError(t, err)
	require.NotEmpty(t, first.GoalID)
	require.Equal(t, "stable-child", first.JobID)
	require.Equal(t, "t", first.TenantID)
	celltest.Create(t, engine, request.TenantId, request.Cell, request.CellRepo)
	retry, err := engine.SubmitJobRef(celltest.Context(context.Background()), request)
	require.NoError(t, err)
	require.Equal(t, first, retry)
	require.Zero(t, rt.reads)
}
