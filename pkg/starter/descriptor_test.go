package starter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestDescriptorRetainsWaitsAndAuthoritativeEnvelope(t *testing.T) {
	original := time.Date(2027, 1, 1, 12, 20, 0, 123456789, time.FixedZone("offset", 19800))
	want := original.UTC()
	deps := []jobdb.Dependency{
		{Target: jobdb.DependencyTarget{JobID: "literal,job"}, Condition: jobdb.JobPrereqComplete},
		{Target: jobdb.DependencyTarget{JobID: "success"}, Condition: jobdb.JobPrereqSuccess},
	}
	start := workflowctl.StartJob{TenantId: "t", Cell: "app", CellRepo: "github.com/acme/app", RecipeName: "build", Dependencies: deps, AvailableAt: &original}
	job, err := PrepareRecipeJob(context.Background(), start, StartRecipeJobOptions{})
	require.NoError(t, err)
	original = original.Add(time.Hour)
	require.Equal(t, want, *job.AvailableAt)
	require.Same(t, time.UTC, job.AvailableAt.Location())
	var input workflowctl.StartJob
	raw, err := job.Data.GetData()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &input))
	require.Equal(t, deps, input.Dependencies)
	require.Equal(t, want, *input.AvailableAt)
	d, err := NewDescriptor(context.Background(), job)
	require.NoError(t, err)
	wire, err := json.Marshal(d)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(wire, &d))
	d.Version = 1
	// Historical input may omit both fields. The executable envelope wins.
	d.Input = json.RawMessage(`{"tenantId":"t","cell":"app","recipe":"build"}`)
	request, err := d.Request()
	require.NoError(t, err)
	require.Equal(t, deps, request.Dependencies)
	require.Equal(t, want, *request.AvailableAt)
	capture := &captureSubmitter{}
	_, err = d.Submit(context.Background(), capture)
	require.NoError(t, err)
	require.Equal(t, deps, capture.job.Dependencies)
	require.Equal(t, want, *capture.job.AvailableAt)
}

func TestExactJobCurrentDescriptorAdmissionBeforePublication(t *testing.T) {
	job, err := PrepareRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "t", Cell: "app", CellRepo: "example.test/app", RecipeName: "build"}, StartRecipeJobOptions{})
	require.NoError(t, err)
	d, err := NewDescriptor(context.Background(), job)
	require.NoError(t, err)
	for _, test := range []struct {
		name   string
		change func(*Descriptor)
	}{
		{"selected", func(d *Descriptor) { d.Job.GoalID = "G" }},
		{"dependency", func(d *Descriptor) {
			d.Job.Dependencies = []jobdb.Dependency{{Target: jobdb.DependencyTarget{GoalID: "G"}, Condition: jobdb.JobPrereqSuccess}}
		}},
		{"unversioned", func(d *Descriptor) { d.Input = json.RawMessage(`{"tenantId":"t"}`) }},
		{"contradictory", func(d *Descriptor) {
			var input workflowctl.StartJob
			require.NoError(t, json.Unmarshal(d.Input, &input))
			input.JobContext.JobSemanticsVersion = 1
			d.Input, err = json.Marshal(input)
			require.NoError(t, err)
		}},
		{"destination", func(d *Descriptor) { d.Job.JobID = "another" }},
		{"raw-cel-recipe", func(d *Descriptor) {
			data := []byte("id: build\nwhen: context.workflow.goal_id != ''\nsequence: []\n")
			d.Artifacts = []DescriptorArtifact{{Name: "build.recipe.yaml", Data: data, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}}
		}},
		{"quoted-marker-cel-recipe", func(d *Descriptor) {
			data := []byte("id: build\nwhen: context.workflow.goal_id != '{{ literal }}'\nsequence: []\n")
			d.Artifacts = []DescriptorArtifact{{Name: "build.recipe.yaml", Data: data, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}}
		}},
		{"static-recipe", func(d *Descriptor) {
			data := []byte("id: build\nversion: '1'\nop: recipe.await_result\ninputs:\n  job_id: A\n  wait:\n    on: goal\n")
			d.Artifacts = []DescriptorArtifact{{Name: "build.recipe.yaml", Data: data, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := d
			test.change(&changed)
			capture := &captureSubmitter{}
			_, err := changed.Submit(context.Background(), capture)
			require.Error(t, err)
			require.Nil(t, capture.job.Data, "admission must precede publication")
		})
	}
}
