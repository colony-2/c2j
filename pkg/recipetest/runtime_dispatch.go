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
	Allow       []DispatchMatcher `json:"allow,omitempty"`
	MaxAttempts int               `json:"max_attempts,omitempty"`
}
type DispatchMatcher struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	NodePath string `json:"node_path,omitempty"`
	Cell     string `json:"cell,omitempty"`
}
type DispatchAttempt struct {
	dispatchtest.Attempt
	Allowed  bool `json:"allowed"`
	Finished bool `json:"finished"`
	Denied   bool `json:"denied"`
	Failed   bool `json:"failed"`
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
			if m.Kind == a.Kind && m.Name == a.Name && (m.NodePath == "" || m.NodePath == a.NodePath) && (m.Cell == "" || m.Cell == a.Cell) {
				allowed = true
			}
		}
	}
	index := len(f.report.Controls.Dispatch)
	f.report.Controls.Dispatch = append(f.report.Controls.Dispatch, DispatchAttempt{Attempt: a, Allowed: allowed, Finished: !allowed, Denied: !allowed, Failed: !allowed})
	f.mu.Unlock()
	if !allowed {
		return nil, f.controlError(fmt.Errorf("forbidden %s dispatch %s at %s", a.Kind, a.Name, a.NodePath))
	}
	return func(err error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		record := &f.report.Controls.Dispatch[index]
		record.Finished = true
		record.Failed = err != nil
		record.Denied = errors.Is(err, jobdb.ErrLeasePermissionDenied) || errors.Is(err, jobdb.ErrPrimaryGoalSelectionForbidden)
	}, nil
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
			NodePath string `json:"node_path"`
		} `json:"context"`
	}
	if readErr = json.Unmarshal(raw, &envelope); readErr != nil {
		return nil, w.f.controlError(fmt.Errorf("dispatch identity decode: %w", readErr))
	}
	a.NodePath = envelope.Context.NodePath
	finish, err := w.f.beginDispatch(tc.Context(), a)
	if err != nil {
		return nil, err
	}
	defer func() { finish(err) }()
	return w.TaskWorker.Run(tc, input)
}
