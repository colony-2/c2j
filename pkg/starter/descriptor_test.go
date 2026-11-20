package starter

import (
	"context"
	"encoding/json"
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
		{Target: jobdb.DependencyTarget{GoalID: "goal"}, Condition: jobdb.JobPrereqSuccess},
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
