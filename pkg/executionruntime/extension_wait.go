package executionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/extensionwait"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/clientpayload"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/segmentio/ksuid"
)

// Kept until the bound result commits: a crash after readiness must not reset
// the original invocation deadline. This contains no extension progress.
type waitGate struct {
	Version      int                   `json:"version"`
	ID           string                `json:"id"`
	Invocation   string                `json:"invocation"`
	Attempt      int                   `json:"attempt"`
	Request      extensionwait.Request `json:"request"`
	NotBefore    time.Time             `json:"not_before"`
	Deadline     *time.Time            `json:"deadline,omitempty"`
	TimeoutScope string                `json:"timeout_scope,omitempty"`
}

func (g *waitGate) attemptID() string { return fmt.Sprintf("%s/attempt:%d", g.Invocation, g.Attempt) }
func (g *waitGate) timeout() error {
	return jobdb.NewTimeoutError("scope", 0, g.TimeoutScope, nil, g.TimeoutScope == jobdb.TimeoutScopeInvocation)
}

func payloadGate(raw json.RawMessage) (*waitGate, error) {
	var root map[string]json.RawMessage
	if len(raw) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		if json.Valid(raw) {
			return nil, nil
		}
		return nil, errors.New("invalid extension wait client payload")
	}
	var ns map[string]json.RawMessage
	if v, ok := root["c2j"]; ok {
		if json.Unmarshal(v, &ns) != nil || ns == nil {
			return nil, errors.New("invalid c2j client payload namespace")
		}
	}
	v, ok := ns["extension_wait"]
	if !ok {
		return nil, nil
	}
	var g waitGate
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.DisallowUnknownFields()
	if dec.Decode(&g) != nil || g.Version != 1 || jobdb.ValidateIdentifier(g.ID) != nil || g.Invocation == "" || g.Attempt < 1 || g.NotBefore.IsZero() {
		return nil, errors.New("invalid extension wait gate")
	}
	if (g.Deadline == nil && g.TimeoutScope != "") || (g.Deadline != nil && (g.Deadline.IsZero() || (g.TimeoutScope != jobdb.TimeoutScopeTotal && g.TimeoutScope != jobdb.TimeoutScopeInvocation))) {
		return nil, errors.New("invalid extension wait deadline")
	}
	request, err := json.Marshal(map[string]any{"wait": g.Request})
	if err != nil {
		return nil, errors.New("invalid extension wait request")
	}
	if _, err := extensionwait.Decode(request, clientpayload.MaxBytes); err != nil {
		return nil, errors.New("invalid extension wait request")
	}
	return &g, nil
}
func payloadWithGate(raw json.RawMessage, gate *waitGate) (json.RawMessage, error) {
	var root map[string]json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, err
		}
	}
	if root == nil {
		root = map[string]json.RawMessage{}
	}
	var ns map[string]json.RawMessage
	if v, ok := root["c2j"]; ok {
		if json.Unmarshal(v, &ns) != nil || ns == nil {
			return nil, errors.New("invalid c2j client payload namespace")
		}
	}
	if ns == nil {
		ns = map[string]json.RawMessage{}
	}
	if gate == nil {
		delete(ns, "extension_wait")
	} else {
		v, err := json.Marshal(gate)
		if err != nil {
			return nil, err
		}
		ns["extension_wait"] = v
	}
	v, err := json.Marshal(ns)
	if err != nil {
		return nil, err
	}
	root["c2j"] = v
	return json.Marshal(root)
}

func (l *lease) current() (*lease, error) {
	l.runtime.mu.Lock()
	defer l.runtime.mu.Unlock()
	current := l.runtime.leases[l.Job().JobKey]
	if current == nil || current.leaseState != l.leaseState {
		return nil, jobdb.ErrExecutionLeaseLost
	}
	return current, nil
}

// The engine writes its path checkpoint before calling the lease's Reschedule.
// Decode both chapters and their compact declaration prefixes, pinned to v4.
// Fail closed if the backend changes that representation.
func (l *lease) waitHistory(ctx context.Context) ([]jobdb.ExecutionRecord, error) {
	current, err := l.current()
	if err != nil {
		return nil, err
	}
	chapters, err := l.runtime.ConsumerRuntime.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: l.Job().JobKey, LeaseID: current.LeaseID(), LeaseToken: current.LeaseToken()})
	if err != nil {
		return nil, errors.New("cannot read extension wait deadline history")
	}
	var records []jobdb.ExecutionRecord
	for _, ch := range chapters {
		var prefix []json.RawMessage
		prefixRaw := ch.Metadata.Fields["metadata"].Map["__execution_prefix_v3"].String
		body, isExecution := ch.Body.(jobdb.ExecutionChapter)
		var rec jobdb.ExecutionRecord
		if isExecution {
			rec, err = jobdb.DecodeExecutionRecord(body.Record)
			if err != nil {
				return nil, errors.New("invalid extension wait execution history")
			}
			if rec.Kind == "restart" && len(rec.Descriptor) > 0 {
				prefixRaw = string(rec.Descriptor)
			}
		}
		if prefixRaw != "" && json.Unmarshal([]byte(prefixRaw), &prefix) != nil {
			return nil, errors.New("invalid extension wait declaration history")
		}
		for _, raw := range prefix {
			r, err := jobdb.DecodeExecutionRecord(raw)
			if err != nil {
				return nil, errors.New("invalid extension wait declaration")
			}
			records = append(records, r)
		}
		if isExecution {
			records = append(records, rec)
		}
	}
	return records, nil
}
func gateCommitted(records []jobdb.ExecutionRecord, g *waitGate) bool {
	for _, r := range records {
		if r.Kind == "result" && r.ID == g.attemptID() {
			return true
		}
	}
	return false
}
func bindDeadline(records []jobdb.ExecutionRecord, g *waitGate) error {
	aid := g.attemptID()
	foundTask, foundAttempt := false, false
	// Continuation budgets intentionally replace the copied deadlines at a human
	// restart. The active invocation's IDs include every enclosing retry attempt.
	type budget struct {
		Deadline time.Time `json:"deadline"`
		Total    time.Time `json:"total_deadline"`
		Attempt  int       `json:"attempt"`
	}
	budgets := map[string]budget{}
	for _, r := range records {
		if r.Kind == "continuation" {
			var next map[string]budget
			if json.Unmarshal(r.Payload, &next) != nil {
				return errors.New("invalid extension wait restart budgets")
			}
			for id, b := range next {
				budgets[id] = b
			}
		}
	}
	apply := func(at time.Time, scope string) {
		if !at.IsZero() && (g.Deadline == nil || at.Before(*g.Deadline)) {
			t := at
			g.Deadline = &t
			g.TimeoutScope = scope
		}
	}
	type policy struct {
		Total      int64 `json:"total_timeout"`
		Invocation int64 `json:"attempt_timeout"`
	}
	policies := map[string]policy{}
	for _, r := range records {
		if r.Kind != "scope" && r.Kind != "task" {
			continue
		}
		if r.ID != g.Invocation && !strings.HasPrefix(aid, r.ID+"/") {
			continue
		}
		var p policy
		if r.Kind == "scope" {
			if json.Unmarshal(r.Descriptor, &p) != nil {
				return errors.New("invalid extension wait scope policy")
			}
		} else {
			var task struct {
				Policy policy `json:"policy"`
			}
			if json.Unmarshal(r.Descriptor, &task) != nil {
				return errors.New("invalid extension wait task policy")
			}
			p = task.Policy
		}
		policies[r.ID] = p
		total := r.TotalDeadline
		if b, ok := budgets[r.ID]; ok {
			total = b.Total
		}
		if p.Total > 0 && total.IsZero() {
			return errors.New("extension wait total deadline is missing")
		}
	}
	for _, r := range records {
		active := r.ID == aid || r.ID == g.Invocation || strings.HasPrefix(aid, r.ID+"/")
		if !active || r.ID == "" {
			continue
		}
		if r.Kind == "task" && r.ID == g.Invocation {
			foundTask = true
		}
		if r.Kind == "attempt" && r.ID == aid && r.Attempt == g.Attempt {
			foundAttempt = true
		}
		if r.Kind != "scope" && r.Kind != "task" && r.Kind != "attempt" {
			continue
		}
		deadline, total := r.Deadline, r.TotalDeadline
		if b, ok := budgets[r.ID]; ok {
			deadline, total = b.Deadline, b.Total
		}
		if r.Kind == "attempt" {
			pos := strings.LastIndex(r.ID, "/attempt:")
			if pos < 0 {
				return errors.New("invalid extension wait attempt identity")
			}
			if p, ok := policies[r.ID[:pos]]; ok && p.Invocation > 0 && deadline.IsZero() {
				return errors.New("extension wait invocation deadline is missing")
			}
		}
		scope := jobdb.TimeoutScopeInvocation
		if !total.IsZero() && (deadline.IsZero() || !deadline.Before(total)) {
			scope = jobdb.TimeoutScopeTotal
		}
		apply(deadline, scope)
		apply(total, jobdb.TimeoutScopeTotal)
	}
	if !foundTask || !foundAttempt {
		return errors.New("extension wait active task deadline history is missing")
	}
	return nil
}

func (g *waitGate) reschedule(jobType string, dependencies bool) jobdb.RescheduleExecutionRequest {
	req := jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: jobType}}
	if dependencies && len(g.Request.Dependencies) > 0 {
		wait := &jobdb.DependencyWait{WaitID: g.ID, Deadline: g.Deadline}
		for _, d := range g.Request.Dependencies {
			wait.Dependencies = append(wait.Dependencies, jobdb.Dependency{Target: jobdb.DependencyTarget{JobID: d.Job, GoalID: d.Goal}, Condition: jobdb.JobPrereqCondition(d.Condition)})
		}
		req.DependencyWait = wait
	} else {
		at := g.NotBefore
		if g.Request.Until != nil && g.Request.Until.After(at) {
			at = *g.Request.Until
		}
		if g.Deadline != nil && g.Deadline.Before(at) {
			at = *g.Deadline
		}
		req.WaitUntil = &at
	}
	return req
}

func (l *lease) yieldWait(tc wf.TaskContext, g *waitGate, dependencies bool) (jobdb.TaskData, error) {
	l.runtime.mu.Lock()
	l.pendingWait = g
	l.runtime.mu.Unlock()
	err := tc.Yield(context.Background(), wf.YieldRequest{Reschedule: g.reschedule(l.Route().JobType, dependencies)})
	if wf.IsExecutionSuspended(err) {
		return nil, err
	}
	return l.blockExecution(errors.Join(extensionwait.New(g.Request), errors.New("extension wait handoff failed"), err))
}

func (g *waitGate) matchesBinding(key jobdb.JobKey, binding jobdb.DependencyWaitInfo) bool {
	if binding.JobKey != key || binding.WaitID != g.ID || len(binding.Dependencies) != len(g.Request.Dependencies) {
		return false
	}
	if (binding.Deadline == nil) != (g.Deadline == nil) || (g.Deadline != nil && !g.Deadline.Equal(*binding.Deadline)) {
		return false
	}
	// JobDB retains dependency order as part of the binding descriptor. Compare
	// the requested targets, not the jobs that happened to satisfy a goal.
	for i, dependency := range g.Request.Dependencies {
		bound := binding.Dependencies[i]
		if bound.Target.JobID != dependency.Job || bound.Target.GoalID != dependency.Goal || string(bound.Condition) != dependency.Condition {
			return false
		}
	}
	return true
}

// waitAdmission runs before Git, configuration, tools or process setup. Only a
// committed outcome permits a different invocation to ignore a retained gate.
func (l *lease) waitAdmission(local context.Context, tc wf.TaskContext) (*waitGate, bool, error) {
	current, err := l.current()
	if err != nil {
		return nil, false, err
	}
	g, err := payloadGate(current.ClientPayload())
	if err != nil {
		return nil, false, err
	}
	if g == nil {
		return nil, false, nil
	}
	records, err := l.waitHistory(local)
	if err != nil {
		return nil, false, err
	}
	if gateCommitted(records, g) {
		l.runtime.mu.Lock()
		l.retireWait = true
		l.runtime.mu.Unlock()
		return nil, false, nil
	}
	if g.Invocation != tc.InvocationID || g.Attempt != tc.AttemptNumber {
		return nil, false, errors.New("extension wait invocation does not match unfinished task")
	}
	if g.Deadline != nil && !time.Now().Before(*g.Deadline) {
		return nil, false, g.timeout()
	}
	if len(g.Request.Dependencies) > 0 {
		binding, err := l.runtime.ConsumerRuntime.GetDependencyWait(local, jobdb.DependencyWaitKey{JobKey: tc.JobKey, WaitID: g.ID})
		if err != nil {
			return nil, false, errors.New("extension wait dependency binding unavailable")
		}
		if !g.matchesBinding(tc.JobKey, binding) {
			return nil, false, errors.New("extension wait dependency binding does not match gate")
		}
		switch binding.State {
		case jobdb.DependencyPending:
			_, err := l.yieldWait(tc, g, true)
			return nil, true, err
		case jobdb.DependencySatisfied:
		case jobdb.DependencyFailed, jobdb.DependencyCancelled, jobdb.DependencyTimedOut:
			return nil, false, binding.Err()
		default:
			return nil, false, errors.New("invalid extension wait dependency state")
		}
	}
	at := g.NotBefore
	if g.Request.Until != nil && g.Request.Until.After(at) {
		at = *g.Request.Until
	}
	if time.Now().Before(at) {
		_, err := l.yieldWait(tc, g, false)
		return nil, true, err
	}
	return g, false, nil
}

func (l *lease) handleWait(tc wf.TaskContext, err error) (jobdb.TaskData, error) {
	d, ok := extensionwait.Eligible(err)
	if !ok {
		return l.blockExecution(errors.Join(err, errors.New("extension wait discard did not complete"), jobdb.ErrExecutionLeaseLost))
	}
	if tc.InvocationID == "" || tc.AttemptNumber < 1 {
		return l.blockExecution(errors.Join(err, errors.New("extension wait requires a versioned leased task"), jobdb.ErrExecutionLeaseLost))
	}
	g := &waitGate{Version: 1, ID: "extension-wait-" + ksuid.New().String(), Invocation: tc.InvocationID, Attempt: tc.AttemptNumber, Request: d.Request, NotBefore: time.Now().UTC().Add(time.Second)}
	// Carry the first cap forward on repeated waits, even if a recovered engine
	// has restarted its in-memory invocation timer.
	current, e := l.current()
	if e != nil {
		return l.blockExecution(errors.Join(err, e))
	}
	old, e := payloadGate(current.ClientPayload())
	if e != nil {
		return l.blockExecution(errors.Join(err, e))
	}
	if old != nil && old.Invocation == g.Invocation && old.Attempt == g.Attempt {
		g.Deadline, g.TimeoutScope = old.Deadline, old.TimeoutScope
	}
	return l.yieldWait(tc, g, true)
}
