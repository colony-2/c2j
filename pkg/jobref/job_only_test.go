package jobref

import (
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExactJobHandles(t *testing.T) {
	h := jobdb.JobHandle{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, Cell: "app", CellID: "cell-id", CellRepo: "github.com/acme/app"}
	for _, goal := range []string{"", "historical-goal"} {
		t.Run(goal, func(t *testing.T) {
			h.GoalID = goal
			ref, err := FromHandle(h)
			require.NoError(t, err)
			require.Equal(t, goal, ref.GoalID)
			job := jobdb.SubmitJob{TenantId: "tenant", JobID: "job", Cell: "app", CellRepo: "https://github.com/acme/app.git"}
			require.NoError(t, ref.ValidateSubmission(job))
			for _, mutate := range []func(*Ref){func(r *Ref) { r.TenantID = "other" }, func(r *Ref) { r.JobID = "other" }, func(r *Ref) { r.Cell = "other" }, func(r *Ref) { r.CellID = "" }, func(r *Ref) { r.CellRepo = "github.com/acme/other" }} {
				bad := ref
				mutate(&bad)
				require.Error(t, bad.ValidateSubmission(job))
			}
		})
	}
}
