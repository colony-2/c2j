package compiler

import (
	"encoding/json"
	"fmt"

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
func (cellContextWorker) Run(tc jobworkflow.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	h := tc.Job()
	if h.Cell == "" || h.CellID == "" || h.CellRepo == "" {
		return nil, fmt.Errorf("job is missing stored cell ownership")
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
	if err = json.Unmarshal(raw, &cell); err != nil {
		return cell, err
	}
	if cell.Cell == "" || cell.CellID == "" || cell.CellRepo == "" {
		return cell, fmt.Errorf("job is missing recorded cell ownership")
	}
	// Names may change, but stable ownership and the job's repo snapshot cannot.
	owner := ctx.Job()
	if cell.CellID != owner.CellID || cell.CellRepo != owner.CellRepo {
		return cell, fmt.Errorf("recorded cell ownership differs from the job")
	}
	return cell, nil
}
