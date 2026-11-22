package jobinputs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestInputHistoryProtocol(t *testing.T) {
	valid := `{"version":1,"page":{"job_id":"j","snapshot_token":"s","completeness":"partial","large":9007199254740993,"raw":null}}`
	r, e := DecodeResponse(strings.NewReader(valid), "list")
	require.NoError(t, e)
	require.Contains(t, string(r.Page), "9007199254740993")
	require.Contains(t, string(r.Page), `"raw":null`)
	for _, raw := range []string{`{}`, `{"version":9}`, `{"version":1}`, `{"version":1,"page":null}`, `{"version":1,"page":{},"entry":{}}`, valid + ` {}`, "Usage: c2j job", `{"version":1,"page":{"job_id":"j","snapshot_token":"s","completeness":"unknown"}}`} {
		t.Run(raw, func(t *testing.T) { _, e := DecodeResponse(strings.NewReader(raw), "list"); require.Error(t, e) })
	}
}
func TestInputHistorySelectors(t *testing.T) {
	for _, q := range []Request{{Version: 1, Operation: "list", JobID: "j", Limit: -1}, {Version: 1, Operation: "list", JobID: "j", Limit: 501}, {Version: 1, Operation: "show", JobID: "j"}, {Version: 1, Operation: "list", JobID: "j", ArtifactID: "key"}, {Version: 2, Operation: "list", JobID: "j"}, {Version: 1, Operation: "artifact", JobID: "j", EntryID: "e", Snapshot: "s"}, {Version: 1, Operation: "list"}} {
		require.Error(t, q.Validate())
	}
	require.NoError(t, (Request{Version: 1, Operation: "list", JobID: "self", Limit: 500}).Validate())
	require.NoError(t, (Request{Version: 1, Operation: "artifact", JobID: "self", EntryID: "e", Snapshot: "s", ArtifactID: "m"}).Validate())
}
func TestInputHistoryDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		cause error
		code  string
	}{{jobdb.ErrLeasePermissionDenied, "denied"}, {jobdb.ErrExecutionLeaseLost, "lease_lost"}, {context.Canceled, "canceled"}, {context.DeadlineExceeded, "timeout"}} {
		d := Diagnostic("unavailable", "failed", errors.Join(errors.New("wrapper"), tc.cause))
		require.Equal(t, tc.code, d.Code)
		require.ErrorIs(t, d, tc.cause)
		raw, e := json.Marshal(d)
		require.NoError(t, e)
		decoded, e := DecodeError(raw)
		require.NoError(t, e)
		require.ErrorIs(t, decoded, tc.cause)
	}
	d := Diagnostic("read", "read failed", errors.New("fault"))
	outer := Diagnostic("storage", "failed", d)
	var typed *Error
	require.ErrorAs(t, outer, &typed)
	require.Equal(t, "read", typed.Code)
}
