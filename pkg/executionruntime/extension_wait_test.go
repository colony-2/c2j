package executionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/extensionwait"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestExtensionWaitGateContract(t *testing.T) {
	now := time.Now().UTC()
	until := now.Add(time.Hour)
	deadline := now.Add(time.Minute)
	g := &waitGate{Version: 1, ID: "wait-1", Invocation: "task", Attempt: 1, Request: extensionwait.Request{Version: 1, Until: &until, Dependencies: []extensionwait.Dependency{{Job: "j", Condition: "success"}}}, NotBefore: now.Add(time.Second), Deadline: &deadline, TimeoutScope: jobdb.TimeoutScopeTotal}
	t.Run("W31_AND", func(t *testing.T) {
		r := g.reschedule("recipe", true)
		require.NotNil(t, r.DependencyWait)
		require.Nil(t, r.WaitUntil)
		require.NoError(t, jobdb.ValidateDependencyReschedule(r))
		r = g.reschedule("recipe", false)
		require.Nil(t, r.DependencyWait)
		require.Equal(t, deadline, *r.WaitUntil)
		require.Nil(t, r.TaskWait)
	})
	t.Run("W32_W53_floor_deadline", func(t *testing.T) {
		copy := *g
		past := now.Add(-time.Hour)
		copy.Request.Until = &past
		require.Equal(t, copy.NotBefore, *copy.reschedule("recipe", false).WaitUntil)
		copy.Deadline = &now
		require.Equal(t, now, *copy.reschedule("recipe", false).WaitUntil)
	})
	t.Run("W38_payload", func(t *testing.T) {
		raw, err := payloadWithGate(json.RawMessage(`{"application":{"large":9007199254740993123456789},"c2j":{"unknown":42}}`), g)
		require.NoError(t, err)
		require.Contains(t, string(raw), "9007199254740993123456789")
		require.Contains(t, string(raw), `"unknown":42`)
		decoded, err := payloadGate(raw)
		require.NoError(t, err)
		require.Equal(t, g, decoded)
		cleared, err := payloadWithGate(raw, nil)
		require.NoError(t, err)
		require.NotContains(t, string(cleared), "extension_wait")
		require.Contains(t, string(cleared), "9007199254740993123456789")
	})
	t.Run("W41_malformed", func(t *testing.T) {
		for _, v := range []string{`null`, `{}`, `{"version":2}`, `{"version":1,"id":"x","invocation":"t","attempt":1,"not_before":"2000-01-01T00:00:00Z","request":{"version":1}}`} {
			_, err := payloadGate(json.RawMessage(`{"c2j":{"extension_wait":` + v + `}}`))
			require.Error(t, err)
		}
	})
}
func TestExtensionWaitDeadlineHistory(t *testing.T) {
	now := time.Now().UTC()
	task := "$job/attempt:2/task:\"extension\"#1"
	aid := task + "/attempt:3"
	outer := now.Add(time.Minute)
	inner := now.Add(2 * time.Minute)
	records := []jobdb.ExecutionRecord{{Kind: "scope", ID: "$job", Descriptor: json.RawMessage(`{}`), TotalDeadline: now.Add(time.Hour)}, {Kind: "attempt", ID: "$job/attempt:1", Attempt: 1, Deadline: now.Add(-time.Hour)}, {Kind: "attempt", ID: "$job/attempt:2", Attempt: 2, Deadline: outer}, {Kind: "task", ID: task, Descriptor: json.RawMessage(`{}`)}, {Kind: "attempt", ID: aid, Attempt: 3, Deadline: inner}}
	t.Run("W47_active_lineage", func(t *testing.T) {
		g := &waitGate{Invocation: task, Attempt: 3}
		require.NoError(t, bindDeadline(records, g))
		require.Equal(t, outer, *g.Deadline)
		require.Equal(t, jobdb.TimeoutScopeInvocation, g.TimeoutScope)
	})
	t.Run("W47_restart", func(t *testing.T) {
		restartDeadline := now.Add(3 * time.Minute)
		payload, _ := json.Marshal(map[string]any{"$job/attempt:2": map[string]any{"deadline": restartDeadline}})
		r := append(append([]jobdb.ExecutionRecord{}, records...), jobdb.ExecutionRecord{Kind: "continuation", ID: task, Payload: payload})
		g := &waitGate{Invocation: task, Attempt: 3}
		require.NoError(t, bindDeadline(r, g))
		require.Equal(t, inner, *g.Deadline)
	})
	t.Run("W48_missing", func(t *testing.T) {
		g := &waitGate{Invocation: task, Attempt: 4}
		require.Error(t, bindDeadline(records, g))
		g = &waitGate{Invocation: task, Attempt: 3}
		require.NoError(t, bindDeadline([]jobdb.ExecutionRecord{{Kind: "task", ID: task, Descriptor: json.RawMessage(`{}`)}, {Kind: "attempt", ID: aid, Attempt: 3}}, g))
		require.Nil(t, g.Deadline)
	})
	t.Run("W48_unreadable_or_missing_declared_bound", func(t *testing.T) {
		for _, descriptor := range []json.RawMessage{json.RawMessage(`{"policy":`), json.RawMessage(`{"policy":{"total_timeout":1}}`), json.RawMessage(`{"policy":{"attempt_timeout":1}}`)} {
			g := &waitGate{Invocation: task, Attempt: 3}
			r := []jobdb.ExecutionRecord{{Kind: "task", ID: task, Descriptor: descriptor}, {Kind: "attempt", ID: aid, Attempt: 3}}
			require.Error(t, bindDeadline(r, g))
		}
	})
	t.Run("W43_committed", func(t *testing.T) {
		g := &waitGate{Invocation: task, Attempt: 3}
		require.False(t, gateCommitted(records, g))
		r := append(records, jobdb.ExecutionRecord{Kind: "result", ID: aid})
		require.True(t, gateCommitted(r, g))
	})
}
func TestExtensionWaitDemandComposition(t *testing.T) {
	f := newExtensionWaitFixture(t, "toy", extensionwait.Request{}, jobdb.RunPolicy{})
	ctx := celltest.Context(context.Background())
	rt := New(f.rt, allocation("2Gi"))
	acquired, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: f.key, Routes: []jobdb.Route{{JobType: "wait-test"}}, WorkerID: "compose", LeaseDuration: time.Minute})
	require.NoError(t, err)
	l := rt.leases[f.key]
	rt.Stage(f.key, execution.Demand{SchemaVersion: execution.DemandSchemaVersion, RecipeResolution: "resolved", Effective: execution.Requirements{Resources: execution.Resources{Memory: str("2Gi")}}})
	rev := acquired.ClientPayloadRevision()
	req, _, published, err := l.prepareReschedule(context.Background(), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "wait-test"}, ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "reset", Value: json.RawMessage(`{"application":{"large":9007199254740993}}`), ExpectedRevision: &rev}})
	require.NoError(t, err)
	require.True(t, published)
	require.Contains(t, string(req.ClientPayloadUpdate.Value), "9007199254740993")
	require.Contains(t, string(req.ClientPayloadUpdate.Value), `"execution"`)
	wrong := rev + 1
	_, _, _, err = l.prepareReschedule(context.Background(), jobdb.RescheduleExecutionRequest{ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "reset", Value: json.RawMessage(`{}`), ExpectedRevision: &wrong}})
	require.ErrorIs(t, err, jobdb.ErrConflict)
}
func TestExtensionWaitRedactionPreservesDiscard(t *testing.T) {
	e := extensionwait.Cleaned(extensionwait.New(extensionwait.Request{Version: 1}), nil)
	e = errors.Join(e, errors.New(strings.Repeat("x", 10)))
	require.True(t, extensionwait.IsDiscard(e))
	_, ok := extensionwait.Eligible(e)
	require.False(t, ok)
}

func TestExtensionWaitUnsupportedContextAndLeaseLoss(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost=%t", lost), func(t *testing.T) {
			f := newExtensionWaitFixture(t, "toy", extensionwait.Request{}, jobdb.RunPolicy{})
			rt := New(f.rt, execution.Allocation{SchemaVersion: 2})
			_, err := rt.GetJobLease(waitTestContext(context.Background()), jobdb.GetJobLeaseRequest{JobKey: f.key, Routes: []jobdb.Route{{JobType: "wait-test"}}, WorkerID: "unsupported", LeaseDuration: time.Minute})
			require.NoError(t, err)
			l := rt.leases[f.key]
			tc := wf.NewTaskContext(f.key, 1, nil, nil, nil)
			if lost {
				tc.InvocationID, tc.AttemptNumber = "task", 1
				delete(rt.leases, f.key)
			}
			out, err := l.handleWait(tc, extensionwait.Cleaned(extensionwait.New(extensionwait.Request{Version: 1}), nil))
			require.Nil(t, out)
			require.True(t, extensionwait.IsDiscard(err))
			require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
			require.NotNil(t, l.blocked)
			require.Nil(t, f.gate(t))
		})
	}
}

func TestExtensionWaitSchedulingDeclarationsOnly(t *testing.T) {
	until := time.Date(2029, 11, 30, 17, 23, 19, 0, time.UTC)
	f := newExtensionWaitFixture(t, "toy", extensionwait.Request{Version: 1, Until: &until}, jobdb.RunPolicy{})
	out, err := f.run(t)
	require.NoError(t, err)
	require.Equal(t, wf.JobRunSuspended, out.Status)
	chapters, err := f.rt.ListChapters(waitTestContext(context.Background()), jobdb.ListChaptersRequest{JobKey: f.key})
	require.NoError(t, err)
	for _, ch := range chapters {
		if body, ok := ch.Body.(jobdb.ExecutionChapter); ok {
			r, err := jobdb.DecodeExecutionRecord(body.Record)
			require.NoError(t, err)
			require.NotEqual(t, "result", r.Kind)
			require.NotContains(t, string(body.Record), until.Format(time.RFC3339))
			require.NotContains(t, string(body.Record), `"wait"`)
		}
	}
	require.Equal(t, until, *f.gate(t).Request.Until, "only the scheduler gate retains eligibility conditions")
}

type waitLeaseSnapshot struct {
	jobdb.ExecutionLease
	raw      json.RawMessage
	revision int64
}

func (l waitLeaseSnapshot) ClientPayload() json.RawMessage { return l.raw }
func (l waitLeaseSnapshot) ClientPayloadRevision() int64   { return l.revision }
func (l waitLeaseSnapshot) LeaseToken() string {
	if v, ok := l.ExecutionLease.(interface{ LeaseToken() string }); ok {
		return v.LeaseToken()
	}
	return ""
}

func TestExtensionWaitRenewalAndCorruption(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour)
	f := newExtensionWaitFixture(t, "toy", extensionwait.Request{Version: 1, Until: &past}, jobdb.RunPolicy{})
	out, e := f.run(t)
	require.NoError(t, e)
	require.NotNil(t, out.Handoff)
	g := f.gate(t)
	waitReady(t, g)
	rt := New(f.rt, execution.Allocation{SchemaVersion: 2})
	ctx := waitTestContext(context.Background())
	acquired, e := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: f.key, Routes: []jobdb.Route{{JobType: "wait-test"}}, WorkerID: "renewal", LeaseDuration: time.Minute})
	require.NoError(t, e)
	require.NotNil(t, acquired)
	old := rt.leases[f.key]
	raw, e := payloadWithGate(json.RawMessage(`{"unrelated":9007199254740993}`), g)
	require.NoError(t, e)
	rt.wrapLease(waitLeaseSnapshot{old.ExecutionLease, raw, 99}, old.leaseState)
	req, _, _, e := old.prepareReschedule(context.Background(), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "wait-test"}})
	require.NoError(t, e)
	require.EqualValues(t, 99, *req.ClientPayloadUpdate.ExpectedRevision)
	require.Contains(t, string(req.ClientPayloadUpdate.Value), "9007199254740993")
	tc := wf.NewTaskContext(f.key, 123, nil, nil, nil)
	tc.InvocationID = g.Invocation
	tc.AttemptNumber = g.Attempt
	copy := *g
	copy.Invocation = "wrong"
	badRaw, e := payloadWithGate(raw, &copy)
	require.NoError(t, e)
	rt.wrapLease(waitLeaseSnapshot{old.ExecutionLease, badRaw, 99}, old.leaseState)
	_, _, e = old.waitAdmission(ctx, tc)
	require.ErrorContains(t, e, "invocation")
	copy = *g
	copy.Request.Dependencies = []extensionwait.Dependency{{Job: "missing", Condition: "success"}}
	badRaw, e = payloadWithGate(raw, &copy)
	require.NoError(t, e)
	rt.wrapLease(waitLeaseSnapshot{old.ExecutionLease, badRaw, 99}, old.leaseState)
	_, _, e = old.waitAdmission(ctx, tc)
	require.ErrorContains(t, e, "binding unavailable")
}
