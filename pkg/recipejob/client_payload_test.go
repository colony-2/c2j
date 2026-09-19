package recipejob

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestRecipeJobSummarySeparatesClientPayloadFromSubmission(t *testing.T) {
	submitted := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	metadata := starter.JobMetadataFromStartJob(workflowctl.StartJob{
		RecipeName: "declared-recipe", InputHash: "input-hash", SubmittedAt: &submitted,
	})
	raw, err := json.Marshal(metadata)
	require.NoError(t, err)
	for _, payload := range []string{`null`, `["opaque",9007199254740993]`, `{"recipe":"not-job-input","run_policy":"not-framework-state"}`} {
		t.Run(payload, func(t *testing.T) {
			summary := jobdb.JobSummary{
				JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, JobType: starter.RecipeJobType,
				Metadata: raw, ClientPayload: json.RawMessage(payload), ClientPayloadRevision: 4,
			}
			job, ok, err := RecipeJobFromSummary(summary)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, "declared-recipe", job.RecipeName)
			require.Equal(t, "input-hash", job.InputHash)
			require.Equal(t, submitted, *job.SubmittedAt)
			require.Equal(t, payload, string(job.ClientPayload))
			require.EqualValues(t, 4, job.ClientPayloadRevision)
			job.ClientPayload[0] = ' '
			require.Equal(t, payload, string(summary.ClientPayload), "DTO must not alias the source")
		})
	}
}

func TestRecipeJobSummaryPreservesTypedRoute(t *testing.T) {
	summary := jobdb.JobSummary{
		JobType:   starter.RecipeJobType,
		NextRoute: &jobdb.Route{JobType: "recipe:kind", TaskType: "input:collect_user_input"},
		ExecutionState: jobdb.ExecutionState{TaskWait: &jobdb.TaskWait{
			InputOrdinal: 0, OutputOrdinal: 2, InputHash: "hash", ResumeJobType: "recipe:kind",
		}},
	}
	job, ok, err := RecipeJobFromSummary(summary)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, summary.NextRoute, job.NextRoute)
	require.Equal(t, summary.ExecutionState.TaskWait, job.TaskWait)
	raw, err := json.Marshal(job)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"next_route":{"jobType":"recipe:kind","taskType":"input:collect_user_input"}`)
	require.NotContains(t, string(raw), `"next_need"`)
	job.NextRoute.TaskType = "changed"
	job.TaskWait.ResumeJobType = "changed"
	require.Equal(t, "input:collect_user_input", summary.NextRoute.TaskType)
	require.Equal(t, "recipe:kind", summary.ExecutionState.TaskWait.ResumeJobType)
}
