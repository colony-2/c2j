package commandop

import "github.com/colony-2/c2j/pkg/ops/executiondiag"

// FailureDetails version 2 replaces the version-1 raw stream artifacts with a
// single execution log. Persisted version-1 references remain ordinary artifacts.
type FailureDetails = executiondiag.Details

type commandFailure struct {
	cause   error
	message string
	details FailureDetails
}

func (e *commandFailure) Error() string { return e.message }
func (e *commandFailure) Unwrap() error { return e.cause }
func (e *commandFailure) FailureAttributes() map[string]any {
	return map[string]any{"command": e.details}
}
