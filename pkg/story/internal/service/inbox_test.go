package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type inboxEngine struct {
	jobworkflow.Engine
	run      jobdb.GetJobRunResponse
	err      error
	artifact jobdb.Artifact
	reads    int
	request  jobdb.GetJobRunRequest
}

func (e *inboxEngine) GetJobRun(_ context.Context, req jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error) {
	e.request = req
	return e.run, e.err
}
func (e *inboxEngine) GetArtifact(_ string, _ jobdb.ArtifactKey) (jobdb.Artifact, error) {
	e.reads++
	return e.artifact, e.err
}
func inboxService(t *testing.T, raw string) (*Service, *inboxEngine, model.GetInboxValueRequest) {
	t.Helper()
	req := model.GetInboxValueRequest{ProjectID: "tenant", JobID: "job", JobAttempt: 1, TaskOrdinal: 7, TaskAttempt: 1, Binding: "file"}
	e := &inboxEngine{run: jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}}, Attempts: []jobdb.JobAttempt{{Attempt: 1, Tasks: []jobdb.TaskRun{{Attempts: []jobdb.TaskAttempt{{Ordinal: 7, Attempt: 1, Input: &jobdb.TaskIO{Data: []byte(raw)}}}}}}}}}
	svc, err := New(Config{Engine: e})
	require.NoError(t, err)
	return svc, e, req
}
func TestInboxContentSerialization(t *testing.T) {
	for _, tc := range []struct{ format, value, want string }{
		{"text", `"${{ expression }}\n$(false)"`, "${{ expression }}\n$(false)"},
		{"json", ` { "large": 9007199254740993123456789, "n":null } `, "{\"large\":9007199254740993123456789,\"n\":null}\n"},
		{"json", `null`, "null\n"},
	} {
		t.Run(tc.format+tc.value, func(t *testing.T) {
			ref := artifacts.Ref{Kind: artifacts.RefKindValue, Format: tc.format, Value: json.RawMessage(tc.value)}
			raw, err := json.Marshal(map[string]any{"context": map[string]any{}, "input": nil, "artifacts": map[string]any{"file": ref}})
			require.NoError(t, err)
			svc, e, req := inboxService(t, string(raw))
			got, err := svc.GetInboxValue(context.Background(), req)
			require.NoError(t, err)
			require.Equal(t, tc.want, string(got.Data))
			require.Zero(t, e.reads)
			execution, err := ref.ValueBytes()
			require.NoError(t, err)
			require.Equal(t, execution, got.Data)
		})
	}
}
func TestInboxContentSelectorsAndAttempts(t *testing.T) {
	svc, e, req := inboxService(t, `{"context":{},"input":null,"artifacts":{"file":{"kind":"value","format":"text","value":"first"}}}`)
	old := e.run.Attempts[0]
	e.run.Attempts = append(e.run.Attempts, jobdb.JobAttempt{Attempt: 2, Tasks: []jobdb.TaskRun{{Attempts: []jobdb.TaskAttempt{{Ordinal: 9, Attempt: 2, Input: &jobdb.TaskIO{Data: []byte(`{"context":{},"input":null,"artifacts":{"file":{"kind":"value","format":"text","value":"second"}}}`)}}}}}})
	got, err := svc.GetInboxValue(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "first", string(got.Data))
	req.JobAttempt = 2
	req.TaskOrdinal = 9
	req.TaskAttempt = 2
	got, err = svc.GetInboxValue(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "second", string(got.Data))
	req.JobAttempt = 1
	_, err = svc.GetInboxValue(context.Background(), req)
	require.EqualError(t, err, "inbox: history_missing")
	req.JobAttempt = 0
	_, err = svc.GetInboxValue(context.Background(), req)
	require.EqualError(t, err, "inbox: invalid_selector")
	req.JobAttempt = 1
	req.TaskOrdinal = 7
	req.TaskAttempt = 1
	req.Binding = "unknown"
	_, err = svc.GetInboxValue(context.Background(), req)
	require.EqualError(t, err, "inbox: binding_unknown")
	req.Binding = "file"
	e.run.Attempts = append(e.run.Attempts, old)
	_, err = svc.GetInboxValue(context.Background(), req)
	require.EqualError(t, err, "inbox: invalid_selector")
	require.True(t, e.request.IncludeAttemptInputs)
	require.True(t, e.request.IncludeArtifacts)
}
func TestInboxContentFailures(t *testing.T) {
	for _, tc := range []struct{ descriptor, reason string }{
		{`{"kind":"stored","stored":{"key":{"jobId":"source","taskOrdinal":42,"name":"original"}}}`, "stored_reference"},
		{`{"kind":"external","external":{"url":"https://secret.example/a"}}`, "source_unsupported"},
		{`{"kind":"value","format":"yaml","value":{}}`, "format_unsupported"},
		{`{"kind":"value","format":"text","value":null}`, "descriptor_malformed"},
		{`{"kind":"stored"}`, "descriptor_malformed"},
		{`{"kind":"value","format":"json"}`, "descriptor_malformed"},
	} {
		t.Run(tc.reason+tc.descriptor, func(t *testing.T) {
			svc, e, req := inboxService(t, `{"context":{},"input":null,"artifacts":{"file":`+tc.descriptor+`}}`)
			_, err := svc.GetInboxValue(context.Background(), req)
			require.EqualError(t, err, "inbox: "+tc.reason)
			require.Zero(t, e.reads)
		})
	}
	svc, e, req := inboxService(t, `{"context":{},"input":null,"input_artifact":"private"}`)
	_, err := svc.GetInboxValue(context.Background(), req)
	require.EqualError(t, err, "inbox: attachment_missing")
	e.run.Attempts[0].Tasks[0].Attempts[0].Input.Artifacts = []jobdb.ArtifactInfo{{Name: "private", Key: &jobdb.ArtifactKey{JobId: "job", TaskOrdinal: 7, Name: "private"}}}
	e.artifact = jobdb.NewArtifactFromBytes("private", []byte(`{"input":null,"artifacts":{"file":{"kind":"value","format":"json","value":9007199254740993}}}`))
	got, err := svc.GetInboxValue(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "9007199254740993\n", string(got.Data))
	require.Equal(t, 1, e.reads)
	e.artifact = jobdb.NewArtifactFromBytes("private", []byte(`{"wrong":true}`))
	_, err = svc.GetInboxValue(context.Background(), req)
	require.EqualError(t, err, "inbox: attachment_malformed")
	e.err = errors.New("denied payload must not leak")
	_, err = svc.GetInboxValue(context.Background(), req)
	require.EqualError(t, err, "inbox: history_unreadable")
	e.err = nil
	e.run.Job.JobKey.TenantId = "other"
	_, err = svc.GetInboxValue(context.Background(), req)
	require.ErrorIs(t, err, ErrWorkflowNotInProject)
}

func TestInboxCompactIdentity(t *testing.T) {
	svc, e, req := inboxService(t, `{}`)
	first := `/scope:"$job"#1/attempt:1/task:"first"#1`
	second := `/scope:"$job"#1/attempt:1/task:"second"#1`
	snapshot := func(value string) *jobdb.ExecutionSnapshot {
		return &jobdb.ExecutionSnapshot{Data: []byte(`{"context":{},"input":null,"artifacts":{"file":{"kind":"value","format":"text","value":"` + value + `"}}}`)}
	}
	e.run.Execution = []jobdb.ExecutionHistoryEntry{
		{Ordinal: 7, Record: jobdb.ExecutionRecord{Kind: "result", ID: first + "/attempt:1", Input: snapshot("first")}},
		{Ordinal: 7, Record: jobdb.ExecutionRecord{Kind: "result", ID: second + "/attempt:1", Input: snapshot("second")}},
	}
	_, err := svc.GetInboxValue(context.Background(), req)
	require.EqualError(t, err, "inbox: invalid_selector")
	req.InvocationID = first
	got, err := svc.GetInboxValue(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "first", string(got.Data))
	req.InvocationID = second + "/attempt:1"
	got, err = svc.GetInboxValue(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "second", string(got.Data))
	req.JobAttempt = 2
	_, err = svc.GetInboxValue(context.Background(), req)
	require.EqualError(t, err, "inbox: invalid_selector")
	req.JobAttempt = 1
	e.err = jobdb.ErrLeasePermissionDenied
	_, err = svc.GetInboxValue(context.Background(), req)
	require.EqualError(t, err, "inbox: history_denied")
}
