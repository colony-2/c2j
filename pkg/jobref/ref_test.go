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
	require.Error(t, (Ref{TenantID: "tenant", JobID: "legacy", GoalID: "goal"}).Validate(), "historical decoding alone does not prove captured ownership")
}

func TestExactJobMalformedCapturedIdentity(t *testing.T) {
	valid := Ref{TenantID: "t", JobID: "j", Cell: "app", CellID: "immutable", CellRepo: "example.test/app"}
	for _, value := range []string{"", " ", " leading", "trailing ", "control\n", string([]byte{0xff})} {
		for _, field := range []string{"tenant", "job", "cell ID", "repository"} {
			ref := valid
			switch field {
			case "tenant":
				ref.TenantID = value
			case "job":
				ref.JobID = value
			case "cell ID":
				ref.CellID = value
			case "repository":
				ref.CellRepo = value
			}
			require.Error(t, ref.Validate(), "%s=%q", field, value)
		}
	}
}
