package listjobs

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestWaitingForPreservesOpaqueIdentifiers(t *testing.T) {
	want := []jobdb.JobTaskFilter{
		{JobType: " recipe:Worker,Ω ", TaskType: " input:collect_user_input,Step "},
		{JobType: "recipe", TaskType: "two-step-op:second"},
	}
	got, err := parseWaitingForFilters([]string{
		`{"jobType":" recipe:Worker,Ω ","taskType":" input:collect_user_input,Step "}`,
		`{"jobType":"recipe","taskType":"two-step-op:second"}`,
	})
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestWaitingForRejectsAmbiguousOrInvalidRoutes(t *testing.T) {
	for _, input := range []string{
		"recipe:input:collect_user_input", "", "null", `[]`,
		`{"jobType":"recipe"}`, `{"taskType":"task"}`, `{"jobType":"","taskType":"task"}`,
		`{"jobType":"recipe","taskType":""}`, `{"jobType":"recipe","taskType":"task\u0000"}`,
		`{"jobType":"recipe","taskType":"task","nextNeed":"unexpected"}`,
		`{"jobType":"recipe","taskType":"task"} {}`,
	} {
		t.Run(input, func(t *testing.T) {
			_, err := parseWaitingForFilters([]string{input})
			require.Error(t, err)
			require.Contains(t, err.Error(), "--waiting-for")
		})
	}
}

func TestJobRowUsesTypedRouteAndTaskCoordinates(t *testing.T) {
	summary := jobdb.JobSummary{
		NextRoute: &jobdb.Route{JobType: "recipe:kind", TaskType: "input:collect"},
		ExecutionState: jobdb.ExecutionState{TaskWait: &jobdb.TaskWait{
			InputOrdinal: 0, OutputOrdinal: 3, InputHash: "hash", ResumeJobType: "recipe:kind",
		}},
	}
	row := makeJobRow(summary)
	require.Equal(t, summary.NextRoute, row.NextRoute)
	require.Equal(t, summary.ExecutionState.TaskWait, row.TaskWait)
	require.JSONEq(t, `{"jobType":"recipe:kind","taskType":"input:collect"}`, displayNext(row))
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"next_route":{"jobType":"recipe:kind","taskType":"input:collect"}`)
	require.Contains(t, string(raw), `"inputOrdinal":0`)
	require.Contains(t, string(raw), `"resumeJobType":"recipe:kind"`)
	require.NotContains(t, string(raw), `"next_need"`)
	row.NextRoute.TaskType = "changed"
	row.TaskWait.ResumeJobType = "changed"
	require.Equal(t, "input:collect", summary.NextRoute.TaskType)
	require.Equal(t, "recipe:kind", summary.ExecutionState.TaskWait.ResumeJobType)
}

func TestExecutionRowDoesNotExposeInFlightNeeds(t *testing.T) {
	memory := "16Gi"
	demand, err := execution.Initial(nil, "pinned", execution.Requirements{Resources: execution.Resources{Memory: &memory}})
	require.NoError(t, err)
	payload, err := execution.PayloadWithDemand(nil, demand)
	require.NoError(t, err)
	job := jobdb.JobSummary{Status: jobdb.JobStatusReady, ClientPayload: payload}
	row := makeJobRow(job)
	require.Equal(t, "specified", row.Execution.Status)
	require.NotNil(t, row.Execution.Demand)
	job.Status = jobdb.JobStatusActive
	row = makeJobRow(job)
	require.Equal(t, "in_flight", row.Execution.Status)
	require.Nil(t, row.Execution.Demand)
	require.Nil(t, row.Execution.Initial)
	require.JSONEq(t, string(payload), string(row.ClientPayload), "raw historical client state remains opaque")
}
