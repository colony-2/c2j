package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type historyEngine struct {
	wf.Engine
	run                jobdb.GetJobRunResponse
	readErr, replayErr error
	reads, replays     int
	key                jobdb.JobKey
}

func (e *historyEngine) GetJobRun(ctx context.Context, q jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error) {
	e.reads++
	e.key = q.JobKey
	return e.run, e.readErr
}
func (e *historyEngine) ReplayJobRun(context.Context, wf.ReplayRunRequest) (jobdb.JobData, error) {
	e.replays++
	return nil, e.replayErr
}
func eligible(id string, n int64) jobdb.ExecutionHistoryEntry {
	return jobdb.ExecutionHistoryEntry{Ordinal: n, Record: jobdb.ExecutionRecord{ID: id, Kind: "scope", Descriptor: json.RawMessage(`{"restart":true}`)}}
}
func TestSelectorsAndAmbiguousIdentity(t *testing.T) {
	n := int64(9)
	points := []jobrestart.RestartScope{{ScopeID: "a", ChapterOrdinal: n}, {ScopeID: "b", ChapterOrdinal: n}}
	_, err := Select(points, Selector{Chapter: &n})
	require.ErrorContains(t, err, "2 boundaries")
	point, err := Select(points, Selector{Scope: "a"})
	require.NoError(t, err)
	require.Equal(t, "a", point.ScopeID)
	points = points[:1]
	point, err = Select(points, Selector{Chapter: &n})
	require.NoError(t, err)
	require.Equal(t, "a", point.ScopeID)
	points[0].UnavailableReason = "duplicate identity"
	_, err = Select(points, Selector{Chapter: &n})
	require.ErrorContains(t, err, "duplicate identity")
	_, err = Select(points, Selector{Scope: "missing"})
	require.ErrorContains(t, err, "0 boundaries")
}
func TestDiscoverySurvivesUnavailableReplay(t *testing.T) {
	cause := errors.New("retained recipe source unavailable")
	engine := &historyEngine{replayErr: cause}
	engine.run.Execution = []jobdb.ExecutionHistoryEntry{eligible("a", 9)}
	svc := &Service{Engine: engine, Tenant: "tenant"}
	result, err := svc.Restart(context.Background(), Request{Operation: "points", JobID: "job"})
	require.NoError(t, err)
	require.Len(t, result.Points.RestartScopes, 1)
	require.Zero(t, engine.replays)
	_, err = svc.Restart(context.Background(), Request{Operation: "context", JobID: "job", Selector: Selector{Scope: "a"}})
	require.ErrorIs(t, err, cause)
	require.ErrorContains(t, err, "restart context unavailable")
	require.Equal(t, jobdb.JobKey{TenantId: "tenant", JobId: "job"}, engine.key)
	_, err = svc.Restart(context.Background(), Request{Operation: "submit", JobID: "job", Selector: Selector{Scope: "a"}})
	require.ErrorContains(t, err, "authorized restart submission capability unavailable")
	require.Equal(t, 1, engine.replays)
	engine.run.Execution = nil
	result, err = svc.Restart(context.Background(), Request{Operation: "points", JobID: "job"})
	require.NoError(t, err)
	raw, err := json.Marshal(result.Points)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"restart_scopes":[]`)
}
func TestRestartServiceReadErrorsAndAuthorization(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, jobdb.ErrJobNotFound, jobdb.ErrLeasePermissionDenied} {
		engine := &historyEngine{readErr: cause}
		svc := &Service{Engine: engine, Tenant: "tenant"}
		for _, op := range []string{"points", "context", "submit"} {
			out, err := svc.Restart(context.Background(), Request{Operation: op, JobID: "job", Selector: Selector{Scope: "a"}})
			require.Nil(t, out)
			require.ErrorIs(t, err, cause)
		}
	}
	engine := &historyEngine{}
	svc := &Service{Engine: engine, Tenant: "tenant", Authorize: func(context.Context, string) error { return jobdb.ErrLeasePermissionDenied }}
	_, err := svc.Restart(context.Background(), Request{Operation: "points", JobID: "job"})
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
	require.Zero(t, engine.reads)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.Authorize = nil
	_, err = svc.Restart(ctx, Request{Operation: "points", JobID: "job"})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, engine.reads)
}

// JP-14, JP-20: programmatic invalid patches fail before metadata/retry lookup.
func TestPatchValidationPrecedesBackendReads(t *testing.T) {
	engine := &historyEngine{}
	svc := &Service{Engine: engine, Tenant: "tenant"}
	for _, p := range []task.ContextPatch{
		{{Op: "replace", Path: "/context/workspace", Value: []byte(`null`)}},
		{{Op: "add", Path: "/sequence/s/outputs/x"}},
		{{Op: "copy", From: "/context/git/hash", Path: "/sequence/s/outputs/x"}},
	} {
		_, err := svc.Restart(context.Background(), Request{Operation: "submit", JobID: "job", Selector: Selector{Scope: "scope"}, RequestID: "accepted-retry", Patch: &p})
		require.Error(t, err)
		require.Zero(t, engine.reads)
		require.Zero(t, engine.replays)
	}
	for _, raw := range []string{`{"patch":{"job":{}}}`, `{"patch":null}`, `{"patch":{"scopes":[]}}`} {
		var q Request
		require.Error(t, json.Unmarshal([]byte(raw), &q))
		require.Zero(t, engine.reads)
	}
}
