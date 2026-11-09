package live

import (
	"encoding/json"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRecordedRestartScopesHistoryOnly(t *testing.T) {
	now := time.Now().UTC()
	run := jobdb.GetJobRunResponse{}
	require.Empty(t, RecordedRestartScopes(run))
	run.Job.JobType = "recipe"
	run.Execution = []jobdb.ExecutionHistoryEntry{
		{Ordinal: 1, Record: jobdb.ExecutionRecord{Kind: "scope", ID: "root", At: now, Descriptor: json.RawMessage(`{}`)}},
		{Ordinal: 2, Record: jobdb.ExecutionRecord{Kind: "scope", ID: "visit-1", At: now, Descriptor: json.RawMessage(`{"restart":true,"title":"Loop","path":["loop"]}`)}},
		{Ordinal: 2, Record: jobdb.ExecutionRecord{Kind: "scope", ID: "visit-2", At: now, Descriptor: json.RawMessage(`{"restart":true}`)}},
		{Ordinal: 3, Record: jobdb.ExecutionRecord{Kind: "task", ID: "task", Descriptor: json.RawMessage(`{"restart":true}`)}},
		{Ordinal: 4, Record: jobdb.ExecutionRecord{Kind: "scope", ID: "broken", Descriptor: json.RawMessage(`{`)}},
	}
	points := RecordedRestartScopes(run)
	require.Len(t, points, 3)
	require.Equal(t, "c2j:submission", points[0].ScopeID)
	require.Equal(t, "visit-1", points[1].ScopeID)
	require.Equal(t, "visit-2", points[2].ScopeID)
	require.Equal(t, "Loop", points[1].Title)
	require.Equal(t, []string{"loop"}, points[1].Path)
	require.Equal(t, now, points[1].StartedAt)
	_, err := RestartBoundary(run.Execution, 2)
	require.ErrorContains(t, err, "multiple scopes")
	boundary, err := RestartBoundaryByID(run.Execution, "visit-2")
	require.NoError(t, err)
	require.EqualValues(t, 2, boundary.Ordinal)
	run.Execution = append(run.Execution, run.Execution[1])
	points = RecordedRestartScopes(run)
	require.Len(t, points, 4)
	require.NotEmpty(t, points[1].UnavailableReason)
	require.NotEmpty(t, points[3].UnavailableReason)
}
