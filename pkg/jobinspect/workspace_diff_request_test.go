package jobinspect

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkspaceDiffRequestV01V02S12(t *testing.T) {
	t.Run("standalone_request_round_trip", func(t *testing.T) {
		var q WorkspaceDiffRequest
		require.NoError(t, json.Unmarshal([]byte(`{"job_id":"job","task":"exact","attempt":2}`), &q))
		require.Equal(t, "job", q.JobID)
		require.Equal(t, "exact", *q.Task)
		require.Equal(t, 2, *q.Attempt)
		b, err := json.Marshal(q)
		require.NoError(t, err)
		var copy WorkspaceDiffRequest
		require.NoError(t, json.Unmarshal(b, &copy))
		require.Equal(t, q, copy)
		for _, bad := range []string{`{"job_id":null}`, `{"job_id":"job","attempt":null}`, `{"job_id":"a","job_id":"b"}`} {
			require.Error(t, json.Unmarshal([]byte(bad), &copy))
		}
	})
	for _, raw := range []string{
		`{"operation":"workspace_diff","job_id":"job"}`,
		`{"operation":"workspace_diff","job_id":"job","workspace_diff":{}}`,
		`{"operation":"workspace_diff","job_id":"job","workspace_diff":{"workspace":"main"}}`,
		`{"operation":"workspace_diff","job_id":"job","workspace_diff":{"scope":"/scope:\"quoted/slash\"#1","attempt":2,"boundary":"before"}}`,
	} {
		t.Run("accepted/"+raw, func(t *testing.T) {
			var q Request
			require.NoError(t, json.Unmarshal([]byte(raw), &q))
			require.NoError(t, validateWorkspaceInspection(q))
			encoded, err := json.Marshal(q)
			require.NoError(t, err)
			var roundTrip Request
			require.NoError(t, json.Unmarshal(encoded, &roundTrip))
			require.Equal(t, q, roundTrip)
		})
	}
	for _, fields := range []string{
		`"task":""`, `"task":null`, `"attempt":0`, `"attempt":null`, `"workspace":""`, `"boundary":""`,
		`"workspace_diff":null`, `"workspace_diff":{"task":null}`, `"workspace_diff":{"scope":""}`,
		`"workspace_diff":{"workspace":" "}`, `"workspace_diff":{"task":"id","scope":"id"}`,
		`"workspace_diff":{"task":"id","attempt":0}`, `"workspace_diff":{"task":"id","attempt":-1}`,
		`"workspace_diff":{"task":"id","attempt":1.5}`, `"workspace_diff":{"task":"id","attempt":"1"}`,
		`"workspace_diff":{"attempt":1}`, `"workspace_diff":{"boundary":"after"}`,
		`"workspace_diff":{"task":"id","boundary":"later"}`, `"workspace_diff":{"task":"id","task":"id"}`,
		`"workspace_diff":{"task":"id","extra":true}`, `"workspace_diff":{},"workspace_diff":{}`,
		`"limit":0`, `"cursor":""`, `"name":""`, `"reference":null`, `"metrics":null`, `"extra":true`,
	} {
		t.Run("rejected/"+fields, func(t *testing.T) {
			var q Request
			require.Error(t, json.Unmarshal([]byte(`{"operation":"workspace_diff","job_id":"job",`+fields+`}`), &q))
		})
	}
	for _, q := range []Request{
		{Operation: "workspace_diff"},
		{Operation: "workspace_diff", JobID: "job", Attempt: 1},
		{Operation: "workspace_diff", JobID: "job", Task: "id"},
		{Operation: "workspace_diff", JobID: "job", Workspace: "main"},
	} {
		require.Error(t, validateWorkspaceInspection(q))
	}
	// The strict operation does not redefine generic operations' null/zero rules.
	var legacy Request
	require.NoError(t, json.Unmarshal([]byte(`{"operation":"log","job_id":"job","task":null,"attempt":0}`), &legacy))
}
