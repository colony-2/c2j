package recipetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/colony-2/c2j/internal/dispatchtest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type DispatchPolicy struct {
	Strict      bool              `json:"strict"`
	Forbid      []DispatchMatcher `json:"forbid,omitempty"`
	Allow       []DispatchMatcher `json:"allow,omitempty"`
	MaxAttempts int               `json:"max_attempts,omitempty"`
}
type DispatchMatcher struct {
	Job      string `json:"job,omitempty"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	NodePath string `json:"node_path,omitempty"`
	Cell     string `json:"cell,omitempty"`
}
type DispatchAttempt struct {
	dispatchtest.Attempt
	NativeCancellation bool   `json:"native_cancellation"`
	CancellationCause  string `json:"cancellation_cause,omitempty"`
	Held               bool   `json:"held"`
	Entered            bool   `json:"entered"`
	Allowed            bool   `json:"allowed"`
	Finished           bool   `json:"finished"`
	Denied             bool   `json:"denied"`
	Failed             bool   `json:"failed"`
}

func (f *runtimeFixture) beginDispatch(ctx context.Context, a dispatchtest.Attempt) (func(error), error) {
	f.mu.Lock()
	// Bind helper attempts to the task envelope already observed before the
	// authorization guard. Never infer an authored node from invocation text.
	if a.Kind == "native" && a.NodePath == "" {
		for i := len(f.report.Controls.Dispatch) - 1; i >= 0; i-- {
			task := f.report.Controls.Dispatch[i]
			if task.Kind == "task" && task.JobID == a.JobID && task.InvocationID == a.InvocationID && task.Attempt.Attempt == a.Attempt && task.ResultOrdinal == a.ResultOrdinal {
				a.NodePath = task.NodePath
				break
			}
		}
	}
	policy := f.c.Runtime.Controls.Dispatch
	limit := 1024
	if policy != nil && policy.MaxAttempts > 0 {
		limit = policy.MaxAttempts
	}
	if len(f.report.Controls.Dispatch) >= limit {
		f.report.Controls.DispatchDropped++
		f.mu.Unlock()
		return nil, f.controlError(fmt.Errorf("dispatch observation limit exceeded; attempted effects are incomplete"))
	}
	allowed := policy == nil || !policy.Strict
	if policy != nil {
		for _, m := range policy.Allow {
			if f.matchesDispatch(m, a) {
				allowed = true
			}
		}
	}
	if policy != nil {
		for _, m := range policy.Forbid {
			if f.matchesDispatch(m, a) {
				allowed = false
			}
		}
	}
	index := len(f.report.Controls.Dispatch)
	f.report.Controls.Dispatch = append(f.report.Controls.Dispatch, DispatchAttempt{Attempt: a, Allowed: allowed, Finished: !allowed, Denied: !allowed, Failed: !allowed})
	f.mu.Unlock()
	if !allowed {
		return nil, f.controlError(fmt.Errorf("forbidden %s dispatch %s at %s", a.Kind, a.Name, a.NodePath))
	}
	finish := func(err error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		record := &f.report.Controls.Dispatch[index]
		record.Finished = true
		record.Failed = err != nil
		if err != nil && ctx.Err() != nil {
			cause := context.Cause(ctx)
			record.NativeCancellation = errors.Is(cause, jobdb.ErrExecutionLeaseLost) || errors.Is(cause, jobdb.ErrJobCancelled)
			record.CancellationCause = fmt.Sprint(cause)
		}
		record.Denied = errors.Is(err, jobdb.ErrLeasePermissionDenied) || errors.Is(err, jobdb.ErrPrimaryGoalSelectionForbidden)
	}
	if a.Kind == "task" && f.scenario != nil {
		if err := f.holdDispatch(ctx, a, a.ScopeID, index); err != nil {
			finish(err)
			return nil, err
		}
	} else {
		f.mu.Lock()
		f.report.Controls.Dispatch[index].Entered = true
		f.mu.Unlock()
	}
	return finish, nil
}

// This wrapper sits outside executionruntime's environment/permission guard,
// so rejected attempts are retained even when no operation body is entered.
type dispatchTaskWorker struct {
	jobworkflow.TaskWorker
	f *runtimeFixture
}

func (w dispatchTaskWorker) Run(tc jobworkflow.TaskContext, input jobdb.TaskData) (out jobdb.TaskData, err error) {
	a := dispatchtest.Attempt{Kind: "task", Name: w.Name(), JobID: tc.JobKey.JobId, InvocationID: tc.InvocationID, Attempt: tc.AttemptNumber, ResultOrdinal: tc.Step, Cell: tc.Job().Cell}
	raw, readErr := input.GetData()
	if readErr != nil {
		return nil, w.f.controlError(fmt.Errorf("dispatch identity unavailable: %w", readErr))
	}
	// Internal preparation tasks have different payloads. Decode only the
	// authored context fields shared by ordinary operation task envelopes.
	var envelope struct {
		Context struct {
			NodePath  string `json:"node_path"`
			Workspace *struct {
				ScopeID string `json:"scope_id"`
			} `json:"workspace"`
		} `json:"context"`
	}
	if readErr = json.Unmarshal(raw, &envelope); readErr != nil {
		return nil, w.f.controlError(fmt.Errorf("dispatch identity decode: %w", readErr))
	}
	a.NodePath = envelope.Context.NodePath
	if envelope.Context.Workspace != nil {
		a.ScopeID = envelope.Context.Workspace.ScopeID
	}
	if w.f.scenario != nil && w.Name() == "recipe_workspace_resolve" {
		var request struct {
			ScopeID string `json:"scope_id"`
		}
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, w.f.controlError(err)
		}
		a.ScopeID = request.ScopeID
		w.f.mu.Lock()
		association := w.f.scenario.workspaces[a.JobID+":"+request.ScopeID]
		w.f.mu.Unlock()
		a.NodePath = association.NodePath
	}
	finish, err := w.f.beginDispatch(tc.Context(), a)
	if err != nil {
		return nil, err
	}
	defer func() { finish(err) }()
	return w.TaskWorker.Run(tc, input)
}

// Called with the fixture mutex held. Named-job policy never matches peers.
func (f *runtimeFixture) matchesDispatch(m DispatchMatcher, a dispatchtest.Attempt) bool {
	if m.Kind != a.Kind || m.Name != a.Name || (m.NodePath != "" && m.NodePath != a.NodePath) || (m.Cell != "" && m.Cell != a.Cell) {
		return false
	}
	if m.Job != "" {
		return f.scenario != nil && a.JobID != "" && f.scenario.report.Jobs[m.Job].Ref.JobID == a.JobID
	}
	return true
}

// A real submission can dispatch before the controller binds its alias. Keep
// the original entry/denial evidence, but never accept absence for that job if
// an earlier allowed attempt now matches a prohibition. Workers and binding
// share the mutex, so an attempt is checked either here or at beginDispatch.
func (f *runtimeFixture) reconcileDispatchForbids() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	policy := f.c.Runtime.Controls.Dispatch
	if policy == nil {
		return nil
	}
	for _, d := range f.report.Controls.Dispatch {
		if !d.Allowed { // Already rejected and recorded by beginDispatch.
			continue
		}
		for _, m := range policy.Forbid {
			if f.matchesDispatch(m, d.Attempt) {
				return fmt.Errorf("forbidden %s dispatch %s at %s for job %s (%s) before alias binding", d.Kind, d.Name, d.NodePath, m.Job, d.JobID)
			}
		}
	}
	return nil
}
