// Package dispatchtest provides internal, context-local test observation at
// dispatch boundaries. No observer is installed during ordinary execution.
package dispatchtest

import "context"

type Attempt struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Cell          string `json:"cell,omitempty"`
	NodePath      string `json:"node_path,omitempty"`
	JobID         string `json:"job_id,omitempty"`
	InvocationID  string `json:"invocation_id,omitempty"`
	Attempt       int    `json:"attempt,omitempty"`
	ResultOrdinal int64  `json:"result_ordinal,omitempty"`
}

type Observer func(context.Context, Attempt) (finish func(error), err error)
type observerKey struct{}
type identityKey struct{}

func WithObserver(ctx context.Context, observer Observer) context.Context {
	return context.WithValue(ctx, observerKey{}, observer)
}

// WithIdentity binds trusted native identity before an access scope is created.
func WithIdentity(ctx context.Context, identity Attempt) context.Context {
	if _, ok := ctx.Value(observerKey{}).(Observer); !ok {
		return ctx
	}
	return context.WithValue(ctx, identityKey{}, identity)
}

func Begin(ctx context.Context, attempt Attempt) (func(error), error) {
	observer, _ := ctx.Value(observerKey{}).(Observer)
	if observer == nil {
		return func(error) {}, nil
	}
	if identity, ok := ctx.Value(identityKey{}).(Attempt); ok {
		attempt.JobID = identity.JobID
		attempt.InvocationID = identity.InvocationID
		attempt.Attempt = identity.Attempt
		attempt.ResultOrdinal = identity.ResultOrdinal
		if attempt.NodePath == "" {
			attempt.NodePath = identity.NodePath
		}
	}
	return observer(ctx, attempt)
}

func Enabled(ctx context.Context) bool { _, ok := ctx.Value(observerKey{}).(Observer); return ok }
