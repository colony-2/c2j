package jobref

import (
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSelectedReceiptAllSixFieldsRequired(t *testing.T) {
	job := jobdb.SubmitJob{TenantId: "tenant", JobID: "dest", GoalID: "goal"}
	complete := Ref{TenantID: "tenant", JobID: "dest", GoalID: "goal", Cell: "captured", CellID: "immutable", CellRepo: "example.test/captured"}
	require.NoError(t, complete.ValidateSelected(job))
	for _, mutate := range []func(*Ref){func(r *Ref) { r.TenantID = "" }, func(r *Ref) { r.JobID = "" }, func(r *Ref) { r.GoalID = "" }, func(r *Ref) { r.Cell = "" }, func(r *Ref) { r.CellID = "" }, func(r *Ref) { r.CellRepo = "" }, func(r *Ref) { r.GoalID = "other" }, func(r *Ref) { r.JobID = "other" }, func(r *Ref) { r.TenantID = "other" }} {
		ref := complete
		mutate(&ref)
		require.ErrorIs(t, ref.ValidateSelected(job), jobdb.ErrSubmissionReceiptUnavailable)
	}
	require.NoError(t, (Ref{TenantID: "tenant", JobID: "legacy", GoalID: "goal"}).Validate(), "ordinary historical references remain valid")
}
