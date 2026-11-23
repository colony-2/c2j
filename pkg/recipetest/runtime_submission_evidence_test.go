package recipetest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	inputop "github.com/colony-2/c2j/pkg/input"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestResponseSubmissionDurableReadStates(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()
	p := PendingResponseIdentity{TenantID: "tenant", JobID: "job", RequestID: "request", OutputOrdinal: 42, Task: TaskSelection{InvocationID: "exact-native-invocation", Attempt: 2}}
	unreadable := readResponseReceipt(context.Background(), nil, db, p, "3")
	require.Equal(t, "unreadable", unreadable.Status)
	require.NotEmpty(t, unreadable.Error)
	_, err = db.Exec(`CREATE TABLE jobdb_chapter_chapters(anthology_id TEXT,story_id TEXT,ordinal INTEGER)`)
	require.NoError(t, err)
	absent := readResponseReceipt(context.Background(), nil, db, p, "3")
	require.Equal(t, "absent", absent.Status)
	require.Nil(t, absent.Receipt)
	receipt := &inputop.Receipt{RequestID: p.RequestID, SubmissionID: "3", Actor: inputop.Actor{ID: "real-actor", Kind: "human"}, SubmittedAt: time.Now().UTC()}
	chapter := func(out inputop.Output) jobdb.Chapter {
		m := map[string]any{}
		raw, e := json.Marshal(out)
		require.NoError(t, e)
		require.NoError(t, json.Unmarshal(raw, &m))
		env, e := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{OpOutput: m})
		require.NoError(t, e)
		data, e := json.Marshal(env)
		require.NoError(t, e)
		record := jobdb.ExecutionRecord{Version: jobdb.ExecutionPatternVersion, Kind: "result", ID: p.Task.InvocationID + "/attempt:2", At: time.Now().UTC(), Output: &jobdb.ExecutionSnapshot{Data: data}}
		raw, e = json.Marshal(record)
		require.NoError(t, e)
		return jobdb.Chapter{Ordinal: 42, Body: jobdb.ExecutionChapter{Record: raw}}
	}
	// A receipt-shaped request/response is never accepted as an output receipt.
	got := decodeSelectedResponseReceipt(chapter(inputop.Output{Response: map[string]any{"receipt": receipt}}), p, "3", 42)
	require.Equal(t, "incomplete", got.Status)
	require.Nil(t, got.Receipt)
	got = decodeSelectedResponseReceipt(chapter(inputop.Output{Receipt: receipt}), p, "3", 42)
	require.Equal(t, "present", got.Status)
	require.Equal(t, receipt, got.Receipt)
	for _, wrong := range []*inputop.Receipt{{RequestID: "other", SubmissionID: "3"}, {RequestID: p.RequestID, SubmissionID: "other"}} {
		got = decodeSelectedResponseReceipt(chapter(inputop.Output{Receipt: wrong}), p, "3", 42)
		require.Equal(t, "incomplete", got.Status)
	}
	got = decodeSelectedResponseReceipt(jobdb.Chapter{Body: jobdb.ExecutionChapter{Record: []byte(`{bad`)}}, p, "3", 42)
	require.Equal(t, "incomplete", got.Status)
}
func TestResponseSubmissionEvidenceSerialization(t *testing.T) {
	a := SubmissionAttempt{Version: 1, ID: "fixture-label", SubmissionID: "7", Pending: PendingResponseIdentity{Task: TaskSelection{Attempt: 2, Occurrence: 3}, RequestID: "request"}, APIError: "storage returned no output", ErrorKind: "storage", ReturnedReceipt: nil, Durable: DurableResponseReceipt{Status: "present", Receipt: &inputop.Receipt{RequestID: "request", SubmissionID: "7"}}, After: &ResponseStateSnapshot{Status: "ACTIVE", Actionable: false, HandlePresent: false}, Checkpoints: []ResponseCheckpoint{{RecoveryMode: "worker_replay", State: ResponseStateSnapshot{Status: "COMPLETED"}}}}
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	require.Nil(t, m["returned_receipt"])
	require.Equal(t, "storage", m["error_kind"])
	require.Equal(t, "7", m["submission_id"])
	require.Equal(t, float64(2), m["pending"].(map[string]any)["task"].(map[string]any)["attempt"])
	require.Equal(t, "present", m["durable"].(map[string]any)["status"])
}
func TestResponseSubmissionIncompleteEvidenceCannotMatchControlFailure(t *testing.T) {
	require.False(t, matchesResponseError(&ResponseExpectedError{Kind: "storage"}, errors.New(inputop.ErrStorage.Error())))
	c, _ := responsePersistenceCase(false, "healthy")
	c.Runtime.Secrets = map[string]string{"TOKEN": "secret\nwith-escapes"}
	f := &runtimeFixture{c: c}
	require.ErrorContains(t, f.requireExactResponseEvidence(map[string]any{"nested": []any{"secret\nwith-escapes"}}), "redaction")
}
