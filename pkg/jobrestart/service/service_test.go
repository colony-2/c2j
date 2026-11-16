package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story/live"
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
	artifactReads      int
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

func (e *historyEngine) GetArtifact(string, jobdb.ArtifactKey) (jobdb.Artifact, error) {
	e.artifactReads++
	return nil, errors.New("artifact body access forbidden")
}

func TestSubmissionOnlyDiscoveryAndContext(t *testing.T) {
	zero := int64(0)
	for _, fixture := range []struct {
		input *jobdb.TaskIO
		valid bool
	}{
		{nil, false}, {&jobdb.TaskIO{}, false}, {&jobdb.TaskIO{Data: json.RawMessage(`null`)}, false},
		{&jobdb.TaskIO{Data: json.RawMessage(`{"inputs":`)}, false},
		{&jobdb.TaskIO{Data: json.RawMessage(`{"inputs":{"value":"original","api_key":"secret"}}`)}, true},
	} {
		input := fixture.input
		engine := &historyEngine{run: jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{JobType: "recipe"}, Start: jobdb.JobStart{Input: input}}}
		svc := &Service{Engine: engine, Tenant: "tenant"}
		points, err := svc.Restart(context.Background(), Request{Operation: "points", JobID: "job"})
		require.NoError(t, err)
		require.Len(t, points.Points.RestartScopes, 1)
		require.Equal(t, 1, engine.reads)
		require.Zero(t, engine.replays)
		require.Zero(t, engine.artifactReads)
		for _, selector := range []Selector{{Scope: "c2j:submission"}, {Chapter: &zero}} {
			engine.reads = 0
			result, err := svc.Restart(context.Background(), Request{Operation: "context", JobID: "job", Selector: selector})
			if fixture.valid {
				require.NoError(t, err)
				require.Equal(t, map[string]any{"value": "original", "api_key": "[REDACTED]"}, result.Context.Visible["inputs"])
			} else {
				require.ErrorIs(t, err, live.ErrRestartContextUnavailable)
			}
			require.Equal(t, 1, engine.reads)
			require.Zero(t, engine.replays)
			require.Zero(t, engine.artifactReads)
			_, err = svc.Restart(context.Background(), Request{Operation: "submit", JobID: "job", Selector: selector})
			require.ErrorContains(t, err, "authorized restart submission capability unavailable")
			submits := 0
			svc.Submit = func(context.Context, jobdb.JobKey, string, *task.ContextPatch, starter.RestartOptions) (jobref.Ref, error) {
				submits++
				return jobref.Ref{}, nil
			}
			patch := task.ContextPatch{{Op: "test", Path: "/sequence/step/outputs/value", Value: json.RawMessage(`"original"`)}}
			_, err = svc.Restart(context.Background(), Request{Operation: "submit", JobID: "job", Selector: selector, Patch: &patch})
			require.ErrorContains(t, err, "submission restart does not support")
			require.Zero(t, submits)
			svc.Submit = nil
		}
	}
}

func TestSubmissionOnlySelectorValidation(t *testing.T) {
	zero, negative := int64(0), int64(-1)
	for _, selector := range []Selector{{}, {Scope: "c2j:submission", Chapter: &zero}, {Chapter: &negative}, {Scope: "unknown"}} {
		engine := &historyEngine{run: jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{JobType: "recipe"}}}
		svc := &Service{Engine: engine, Tenant: "tenant", Submit: func(context.Context, jobdb.JobKey, string, *task.ContextPatch, starter.RestartOptions) (jobref.Ref, error) {
			t.Fatal("invalid selector reached submission")
			return jobref.Ref{}, nil
		}}
		_, err := svc.Restart(context.Background(), Request{Operation: "submit", JobID: "job", Selector: selector})
		require.Error(t, err)
		require.Zero(t, engine.replays)
		require.Zero(t, engine.artifactReads)
		if selector.Scope != "unknown" {
			require.Zero(t, engine.reads)
		}
	}
}
