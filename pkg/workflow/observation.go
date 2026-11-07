package workflow

import (
	"context"
	"github.com/colony-2/c2j/pkg/template"
)

// Observation is transient instrumentation, not a persisted runtime outcome.
// Scope completion is emitted only after that scope's outcome is accepted.
// An accepted child does not imply acceptance of its ancestors. Callbacks must
// copy values synchronously and must not mutate the resolver or execution output.
type Observation struct {
	Kind       string
	Scope      *template.ResolutionContext
	Output     template.ExecutionOutput
	Transition template.TransitionData
}

// Observe never changes execution error handling. Instrumentation failures belong
// in its consumer's diagnostics, not in the recipe execution result.
func (c Context) Observe(event Observation) {
	if c.OnObservation != nil {
		c.OnObservation(c.JobContext.Context(), event)
	}
}

type ObservationCallback func(context.Context, Observation)
