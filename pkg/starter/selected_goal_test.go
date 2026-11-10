package starter

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSelectedGoalOptionConflicts(t *testing.T) {
	start := workflowctl.StartJob{TenantId: "tenant", Cell: "work", RecipeName: "build", JobID: "dest", GoalID: "goal"}
	for _, opts := range []StartRecipeJobOptions{{JobID: "different"}, {GoalID: "different"}} {
		capture := &captureSubmitter{}
		_, err := StartRecipeJobWithOptions(context.Background(), start, capture, opts)
		require.ErrorContains(t, err, "conflicting")
		require.Empty(t, capture.job.JobID)
	}
	start.JobID = ""
	_, err := PrepareRecipeJob(context.Background(), start, StartRecipeJobOptions{})
	require.Error(t, err)
	start.JobID = "dest"
	job, err := PrepareRecipeJob(context.Background(), start, StartRecipeJobOptions{GoalID: "goal", JobID: "dest"})
	require.NoError(t, err)
	require.Equal(t, "goal", job.GoalID)
}

type incompleteSelected struct{}

func (incompleteSelected) SubmitJobRef(_ context.Context, j jobdb.SubmitJob) (jobref.Ref, error) {
	return jobref.Ref{TenantID: j.TenantId, JobID: j.JobID, GoalID: j.GoalID}, nil
}
func TestSelectedGoalIncompleteReceiptAndDescriptorIntegrity(t *testing.T) {
	job, err := PrepareRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "tenant", Cell: "work", RecipeName: "build", JobID: "dest", GoalID: "goal", Artifacts: []jobdb.Artifact{jobdb.NewArtifactFromBytes("file", []byte("original"))}}, StartRecipeJobOptions{})
	require.NoError(t, err)
	d, err := NewDescriptor(context.Background(), job)
	require.NoError(t, err)
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	var saved Descriptor
	require.NoError(t, json.Unmarshal(raw, &saved))
	_, err = saved.Submit(context.Background(), incompleteSelected{})
	require.ErrorIs(t, err, jobdb.ErrSubmissionReceiptUnavailable)
	saved.Artifacts[0].Data = []byte("changed")
	_, err = saved.Request()
	require.ErrorContains(t, err, "integrity")
	d.Operation = "restart"
	_, err = d.Request()
	require.Error(t, err)
}
