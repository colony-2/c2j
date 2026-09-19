package listjobs

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestJobRowPreservesClientPayloadAndRevision(t *testing.T) {
	summary := jobdb.JobSummary{ClientPayload: json.RawMessage(`{"large":9007199254740993}`), ClientPayloadRevision: 9}
	row := makeJobRow(summary)
	data, err := json.Marshal(row)
	require.NoError(t, err)
	require.Contains(t, string(data), `"client_payload":{"large":9007199254740993}`)
	require.Contains(t, string(data), `"client_payload_revision":9`)
	row.ClientPayload[0] = ' '
	require.Equal(t, `{"large":9007199254740993}`, string(summary.ClientPayload))
}
