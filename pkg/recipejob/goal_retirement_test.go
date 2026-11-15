package recipejob_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func historicalBlockingWait() *jobdb.DependencyWaitInfo {
	deadline := time.Date(2026, 11, 20, 1, 2, 3, 4, time.UTC)
	return &jobdb.DependencyWaitInfo{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "consumer"}, WaitID: "saved", State: jobdb.DependencyPending, Deadline: &deadline, Dependencies: []jobdb.DependencyResolution{
		{Target: jobdb.DependencyTarget{GoalID: "G"}, Condition: jobdb.JobPrereqSuccess, State: jobdb.DependencySatisfied, SatisfiedByJobID: "B"},
		{Target: jobdb.DependencyTarget{JobID: "literal-pending"}, Condition: jobdb.JobPrereqComplete, State: jobdb.DependencyPending, BlockedReason: "pending literal target"},
	}}
}

func TestC01DetailPreservesReadErrors(t *testing.T) {
	for _, cause := range []error{jobdb.ErrJobNotFound, errors.New("permission denied"), errors.New("storage unavailable")} {
		r := &selectedJobReader{row: jobdb.JobSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, JobType: "recipe", Metadata: json.RawMessage(`{"recipe":"fixture"}`)}, err: cause}
		got, err := recipejob.GetRecipeJob(context.Background(), r, recipejob.GetRecipeJobRequest{TenantID: "tenant", JobID: "job"})
		if errors.Is(cause, jobdb.ErrJobNotFound) {
			require.ErrorIs(t, err, recipejob.ErrJobNotFound)
		} else {
			require.ErrorIs(t, err, cause)
		}
		require.Equal(t, recipejob.RecipeJob{}, got)
		require.Equal(t, 1, r.lists)
		require.Equal(t, 1, r.gets)
	}
}

func TestHistoricalRecipeAndInspectionDecoding(t *testing.T) {
	var detail recipejob.RecipeJob
	raw := `{"goal_id":"H","other_goal_ids":["G"],"client_payload":{"other_goal_ids":["opaque"],"n":9007199254740993}}`
	require.NoError(t, json.Unmarshal([]byte(raw), &detail))
	require.Equal(t, "H", detail.GoalID)
	require.Equal(t, []string{"G"}, detail.OtherGoalIDs)
	require.Equal(t, `{"other_goal_ids":["opaque"],"n":9007199254740993}`, string(detail.ClientPayload))
	var inspection workflowctl.JobInspection
	require.NoError(t, json.Unmarshal([]byte(`{"GoalID":"H","OtherGoalIDs":["G"]}`), &inspection))
	require.Equal(t, "H", inspection.GoalID)
	require.Equal(t, []string{"G"}, inspection.OtherGoalIDs)
}
