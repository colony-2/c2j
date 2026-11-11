package starter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/stretchr/testify/require"
)

func TestExplicitTitleSelection(t *testing.T) {
	for _, explicit := range []string{"", " \n ", "  Café\nUI  ", "${{ context.secrets.private }}", "${{ context.secrets.?private.orValue(\"fallback\") }}"} {
		for _, legacy := range []any{nil, "", " \n ", "legacy", 42, secrets.Reference{Name: "private"}, "${{ context.secrets.private }}"} {
			start := workflowctl.StartJob{Title: explicit, Inputs: map[string]any{"title": legacy}}
			want := ""
			if explicit == "  Café\nUI  " {
				want = explicit
			} else if (explicit == "" || explicit == " \n ") && legacy == "legacy" {
				want = "legacy"
			}
			require.Equal(t, want, JobMetadataFromStartJob(start).Title)
		}
	}
}

func TestExplicitTitleTransportAndDescriptor(t *testing.T) {
	start := workflowctl.StartJob{TenantId: "tenant", Cell: "work", RecipeName: "build", JobID: "child", GoalID: "goal", Title: "  Café\nUI  ", Inputs: map[string]any{"value": "original"}}
	wire, err := json.Marshal(start)
	require.NoError(t, err)
	var received workflowctl.StartJob
	require.NoError(t, json.Unmarshal(wire, &received))
	require.Equal(t, start, received)
	job, err := PrepareRecipeJob(context.Background(), received, StartRecipeJobOptions{})
	require.NoError(t, err)
	d, err := NewDescriptor(context.Background(), job)
	require.NoError(t, err)
	wire, err = json.Marshal(d)
	require.NoError(t, err)
	var retained Descriptor
	require.NoError(t, json.Unmarshal(wire, &retained))
	restored, err := retained.Request()
	require.NoError(t, err)
	require.Equal(t, job.Metadata, restored.Metadata)
	require.Equal(t, job.JobID, restored.JobID)
	require.Equal(t, job.GoalID, restored.GoalID)
	raw, err := restored.Data.GetData()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &received))
	require.Equal(t, start.Title, received.Title)
	require.Equal(t, start.Inputs, received.Inputs)
	require.Equal(t, start.Title, JobMetadataFromStartJob(received).Title)
}
