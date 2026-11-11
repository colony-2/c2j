// Package preparedtest carries case-local test controls across native operation
// preparation. It is internal so scripting cannot become a production API.
package preparedtest

import (
	"context"
)

// Invocation is the payload at the prepared extension launch boundary. Inputs
// have already passed validation, materialization and object hydration. Env is
// the prepared environment; consumers must redact it before retaining evidence.
type Invocation struct {
	Selector         string
	ResolvedSelector string
	Inputs           map[string]any
	Env              map[string]string
}

// Completion returns the extension's stdout protocol bytes, or an application
// error. A false selection leaves the ordinary process launch unchanged.
type Completion func(context.Context, Invocation) (stdout []byte, selected bool, err error)

type completionKey struct{}

func WithCompletion(ctx context.Context, complete Completion) context.Context {
	return context.WithValue(ctx, completionKey{}, complete)
}

func Complete(ctx context.Context, in Invocation) ([]byte, bool, error) {
	complete, _ := ctx.Value(completionKey{}).(Completion)
	if complete == nil {
		return nil, false, nil
	}
	return complete(ctx, in)
}
