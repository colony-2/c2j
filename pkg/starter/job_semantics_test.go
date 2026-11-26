package starter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestExactJobPreparationVersionAndRejections(t *testing.T) {
	start := workflowctl.StartJob{TenantId: "t", Cell: "app", CellRepo: "example.test/app", RecipeName: "build"}
	job, err := PrepareRecipeJob(context.Background(), start, StartRecipeJobOptions{})
	require.NoError(t, err)
	raw, err := job.Data.GetData()
	require.NoError(t, err)
	var saved workflowctl.StartJob
	require.NoError(t, json.Unmarshal(raw, &saved))
	require.Equal(t, 2, saved.JobSemanticsVersion)
	require.Equal(t, 2, saved.JobContext.JobSemanticsVersion)
	require.Zero(t, start.JobSemanticsVersion)
	d, err := NewDescriptor(context.Background(), job)
	require.NoError(t, err)
	require.Equal(t, 2, d.Version)
	for _, v := range []int{1, 3} {
		invalid := start
		invalid.JobSemanticsVersion = v
		_, err := PrepareRecipeJob(context.Background(), invalid, StartRecipeJobOptions{})
		require.Error(t, err)
	}
	start.GoalID = "G"
	_, err = PrepareRecipeJob(context.Background(), start, StartRecipeJobOptions{})
	require.ErrorContains(t, err, "goal selection")
	_, err = RestartRecipeJob(context.Background(), nil, jobdb.JobKey{TenantId: "t", JobId: "source"}, "scope", nil, RestartOptions{Goal: "continue", RequestID: "id"})
	require.ErrorContains(t, err, "goal sharing")
}

func TestRecipeStatusAutomaticallySelectsHistoryFormat(t *testing.T) {
	start := workflowctl.StartJob{TenantId: "tenant", Cell: "test", RecipeName: "build"}
	job, err := PrepareRecipeJob(context.Background(), start, StartRecipeJobOptions{})
	require.NoError(t, err)
	require.Equal(t, RecipeJobType, job.JobType)
	require.NotNil(t, job.Schema)
	var persisted workflowctl.StartJob
	require.NoError(t, json.Unmarshal(job.Data.GetDataOrPanic(), &persisted))
	require.Equal(t, workflowctl.StatusHistoryVersion, persisted.StatusHistoryVersion)
}

// Format-1 reconciliation resends the original operation and validates its
// complete receipt; it never strips mode or re-prepares mutable inputs.
func TestExactJobLegacyDescriptorReplaysOriginal(t *testing.T) {
	t.Setenv("C2J_WRITE_EXECUTION_HOOK_POLICY", "true")
	input := json.RawMessage(`{"tenantId":"t","recipe":"build","inputs":{"n":9007199254740993}}`)
	d := Descriptor{Version: 1, Operation: "submit", Job: jobdb.SubmitJob{TenantId: "t", JobID: "reserved", GoalID: "original-goal", Cell: "app", CellRepo: "example.test/app"}, Input: input}
	frozen, err := json.Marshal(d)
	require.NoError(t, err)
	request, err := d.Request()
	require.NoError(t, err)
	require.Equal(t, "original-goal", request.GoalID)
	require.Equal(t, []byte(input), []byte(request.Data.GetDataOrPanic()))
	capture := &captureSubmitter{}
	_, err = d.Submit(context.Background(), capture)
	require.ErrorIs(t, err, jobdb.ErrSubmissionReceiptUnavailable) // incomplete receipt is never reconstructed
	require.Equal(t, "original-goal", capture.job.GoalID)
	require.Equal(t, []byte(input), []byte(capture.job.Data.GetDataOrPanic()))
	after, err := json.Marshal(d)
	require.NoError(t, err)
	require.Equal(t, frozen, after)
}

func TestHookPolicyWriteGate(t *testing.T) {
	for _, value := range []string{"", "true"} {
		t.Run("write="+value, func(t *testing.T) {
			t.Setenv("C2J_WRITE_EXECUTION_HOOK_POLICY", value)
			ctx := context.Background()
			start := workflowctl.StartJob{TenantId: "tenant", Cell: "app", CellRepo: "example.test/app", RecipeName: "build", HookPolicyVersion: 99}
			job, err := PrepareRecipeJob(ctx, start, StartRecipeJobOptions{})
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(job.Data.GetDataOrPanic(), &fields))
			if value == "true" {
				require.Equal(t, "1", string(fields["hook_policy_version"]))
			} else {
				require.NotContains(t, fields, "hook_policy_version")
			}
			require.NotContains(t, string(fields["context"]), "child_hook_lineage")
			start.JobContext.InheritedPermissions = &permissions.State{Version: 1, ChildPolicy: json.RawMessage(`{"revision":"retained"}`)}
			legacy, err := PrepareRecipeJob(ctx, start, StartRecipeJobOptions{})
			require.NoError(t, err)
			require.NotContains(t, string(legacy.Data.GetDataOrPanic()), "hook_policy_version")
			require.Contains(t, string(legacy.Data.GetDataOrPanic()), "child_policy")
			start.JobContext.InheritedPermissions.ChildHookLineage = &permissions.HookLineage{Repository: "example.test/forged"}
			_, err = PrepareRecipeJob(ctx, start, StartRecipeJobOptions{})
			require.ErrorContains(t, err, "assigned by the controller")
			start.GoalID = "existing"
			_, err = PrepareRecipeJob(ctx, start, StartRecipeJobOptions{})
			require.ErrorContains(t, err, "goal selection is unsupported")
		})
	}
}
