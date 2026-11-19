// Package scenariotest carries case-local observations. An absent observer is
// inert and does not alter native submission, authorization or replay.
package scenariotest

import (
	"context"

	"github.com/colony-2/c2j/pkg/jobref"
)

type Submission struct {
	Index        int        `json:"index"`
	Caller       string     `json:"caller"`
	InvocationID string     `json:"invocation_id"`
	NodePath     string     `json:"node_path"`
	Attempt      int        `json:"attempt"`
	Kind         string     `json:"kind"`
	ScopeID      string     `json:"scope_id,omitempty"`
	TaskType     string     `json:"task_type"`
	Ref          jobref.Ref `json:"ref"`
}
type Workspace struct{ JobID, NodePath, ScopeID, ParentScopeID string }
type Observer struct {
	Submission func(context.Context, Submission) (func(jobref.Ref, error) error, error)
	Workspace  func(Workspace)
}
type key struct{}

func WithObserver(ctx context.Context, o Observer) context.Context {
	return context.WithValue(ctx, key{}, o)
}
func Begin(ctx context.Context, s Submission) (func(jobref.Ref, error) error, error) {
	if o, ok := ctx.Value(key{}).(Observer); ok && o.Submission != nil {
		return o.Submission(ctx, s)
	}
	return func(_ jobref.Ref, err error) error { return err }, nil
}
func Associate(ctx context.Context, w Workspace) {
	if o, ok := ctx.Value(key{}).(Observer); ok && o.Workspace != nil {
		o.Workspace(w)
	}
}
