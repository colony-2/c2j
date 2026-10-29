package listjobs

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestJobRowPreservesRevisionWithoutPayload(t *testing.T) {
	summary := jobdb.JobSummary{ClientPayload: json.RawMessage(`{"large":9007199254740993}`), ClientPayloadRevision: 9}
	row := makeJobRow(summary)
	data, err := json.Marshal(row)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"client_payload":`)
	require.Contains(t, string(data), `"client_payload_revision":9`)
	require.Empty(t, row.ClientPayload)
	require.Equal(t, `{"large":9007199254740993}`, string(summary.ClientPayload))
}
