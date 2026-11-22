package compiler

import (
	"context"
	"fmt"

	"github.com/colony-2/c2j/pkg/metricsruntime"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

const CellContextTaskType = "recipe_cell_context"

// Record the canonical owning name once. Registry renames must not change
// template inputs or task identities while resuming or replaying a job.
type cellContextWorker struct{}

func NewCellContextTaskWorker() jobworkflow.TaskWorker { return cellContextWorker{} }

func (cellContextWorker) Name() string { return CellContextTaskType }
func (w cellContextWorker) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func (cellContextWorker) RunTask(local context.Context, tc jobworkflow.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	metricsruntime.Execution(local)
	h := tc.Job()
	if h.Cell == "" || h.CellID == "" || h.CellRepo == "" {
		return nil, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("job is missing stored cell ownership"), jobdb.ExecutionInfrastructureDurableDecode, "c2j.cell_context", &h.JobKey, nil)
	}
	return jobdb.NewTaskData(jobdb.ResolvedCell{Cell: h.Cell, CellID: h.CellID, CellRepo: h.CellRepo})
}
func recordedCell(ctx jobworkflow.ExecutionContext) (jobdb.ResolvedCell, error) {
	out, err := workflow.ValueCall(ctx, "cell-context", CellContextTaskType, jobdb.RunPolicy{}, struct{}{})
	if err != nil {
		return jobdb.ResolvedCell{}, err
	}
	raw, err := out.GetData()
	if err != nil {
		return jobdb.ResolvedCell{}, err
	}
	var cell jobdb.ResolvedCell
	if err = coretask.DecodeInternal(raw, &cell); err != nil {
		return cell, err
	}
	if cell.Cell == "" || cell.CellID == "" || cell.CellRepo == "" {
		return cell, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("job is missing recorded cell ownership"), jobdb.ExecutionInfrastructureDurableDecode, "c2j.cell_context", nil, nil)
	}
	// Names may change, but stable ownership and the job's repo snapshot cannot.
	owner := ctx.Job()
	if cell.CellID != owner.CellID || cell.CellRepo != owner.CellRepo {
		return cell, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("recorded cell ownership differs from the job"), jobdb.ExecutionInfrastructureDurableDecode, "c2j.cell_context", nil, nil)
	}
	return cell, nil
}
