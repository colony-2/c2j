package starter

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/jobcontext"
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
	start.TenantId = ""
	_, err = PrepareRecipeJob(context.Background(), start, StartRecipeJobOptions{})
	require.Error(t, err)
	start.TenantId = "tenant"
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
	for _, mutate := range []func(*Descriptor){
		func(d *Descriptor) { d.Operation = "restart" },
		func(d *Descriptor) { d.Version = 2 },
		func(d *Descriptor) { d.Job.JobID = "" },
		func(d *Descriptor) { d.Job.TenantId = "" },
	} {
		changed := d
		mutate(&changed)
		_, err = changed.Request()
		require.Error(t, err)
	}
}

func TestSelectedGoalTransportAndStoredInput(t *testing.T) {
	start := workflowctl.StartJob{TenantId: "tenant", Cell: "work", RecipeName: "build", JobID: "dest", GoalID: "goal", Inputs: map[string]interface{}{"goal_id": "user-input"}, Parent: &jobcontext.Parent{GoalID: "parent"}, Artifacts: []jobdb.Artifact{jobdb.NewArtifactFromBytes("file", []byte("original"))}}
	wire, err := json.Marshal(start)
	require.NoError(t, err)
	var received workflowctl.StartJob
	require.NoError(t, json.Unmarshal(wire, &received))
	require.Equal(t, "goal", received.GoalID)
	job, err := PrepareRecipeJob(context.Background(), start, StartRecipeJobOptions{})
	require.NoError(t, err)
	require.Equal(t, "goal", job.GoalID)
	require.Equal(t, "dest", job.JobID)
	require.Equal(t, "tenant", job.TenantId)
	raw, err := job.Data.GetData()
	require.NoError(t, err)
	var input map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &input))
	require.NotContains(t, input, "goal_id")
	require.NoError(t, json.Unmarshal(raw, &received))
	require.Equal(t, "user-input", received.Inputs["goal_id"])
	require.Equal(t, "parent", received.Parent.GoalID)
	require.Contains(t, string(job.Metadata), `"parent_goal_id":"parent"`)
	d, err := NewDescriptor(context.Background(), job)
	require.NoError(t, err)
	// A historical descriptor must not be re-prepared or rebound to today's schema.
	input["goal_id"] = json.RawMessage(`"stale"`)
	d.Input, err = json.Marshal(input)
	require.NoError(t, err)
	d.Job.Schema = &jobdb.JobSchemaSelector{Hash: "sha256:3b77b87e6349ecd231839a13ddfa30d0da797cb019ff56dd81c5b7260c394750"}
	saved, err := json.Marshal(d)
	require.NoError(t, err)
	var retained Descriptor
	require.NoError(t, json.Unmarshal(saved, &retained))
	request, err := retained.Request()
	require.NoError(t, err)
	got, err := request.Data.GetData()
	require.NoError(t, err)
	require.Equal(t, []byte(d.Input), []byte(got))
	require.Equal(t, d.Job.Schema, request.Schema)
	require.Equal(t, job.GoalID, request.GoalID)
	require.Equal(t, job.JobID, request.JobID)
	require.Equal(t, job.TenantId, request.TenantId)
	require.Equal(t, job.Metadata, request.Metadata)
	arts, err := request.Data.GetArtifacts()
	require.NoError(t, err)
	bytes, err := arts[0].Bytes(context.Background())
	require.NoError(t, err)
	require.Equal(t, "original", string(bytes))
}
