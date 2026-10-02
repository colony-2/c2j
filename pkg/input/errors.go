package input

import (
	"context"
	"errors"
	"fmt"

	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

var (
	ErrValidation          = errors.New("invalid input")
	ErrStaleRequest        = errors.New("request_id does not match the pending input")
	ErrDocumentNotFound    = errors.New("document is not part of the review")
	ErrArtifactUnavailable = errors.New("artifact bytes are unavailable")
	ErrStorage             = errors.New("input storage operation failed")
)

// RuntimeError classifies a runtime failure without choosing a transport status.
// Cause remains available through errors.Is/As. A completion storage failure can
// have an ambiguous outcome and does not imply a retry is safe.
type RuntimeError struct {
	Kind       error
	Operation  string
	Field      string
	FieldID    string
	DocumentID string
	Cause      error
}

func (e *RuntimeError) Error() string {
	message := e.Kind.Error()
	if e.Operation != "" {
		message = e.Operation + ": " + message
	}
	if e.DocumentID != "" {
		message += " (" + e.DocumentID + ")"
	}
	if e.Field != "" {
		message = e.Field + ": " + message
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}
func (e *RuntimeError) Is(target error) bool { return target == e.Kind }
func (e *RuntimeError) Unwrap() error        { return e.Cause }

func runtimeError(kind error, operation string, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	return &RuntimeError{Kind: kind, Operation: operation, Cause: cause}
}
func validationError(field, message string) error {
	return ValidationError{Field: field, Message: message}
}

func fieldError(id string, err error) error {
	var runtimeErr *RuntimeError
	if errors.As(err, &runtimeErr) {
		copy := *runtimeErr
		copy.Field, copy.FieldID = "fields."+id, id
		return &copy
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ValidationError{Field: "fields." + id, FieldID: id, Message: err.Error(), Cause: err}
}

func (r *Runtime) finishInput(ctx context.Context, task workflowctl.TaskHandle, data jobdb.TaskData) error {
	err := task.Finish(ctx, data)
	if err == nil {
		return nil
	}
	// Distinguish a concurrent accepted response from a storage failure while the
	// same occurrence remains waiting. Do not assume every conflict is a retry.
	if errors.Is(err, jobdb.ErrConflict) || errors.Is(err, jobdb.ErrJobNotFound) {
		current, lookupErr := r.ctl.GetWaitingTask(ctx, task.JobKey())
		if errors.Is(lookupErr, jobdb.ErrJobNotFound) || errors.Is(lookupErr, ErrInputNotPending) ||
			(lookupErr == nil && (current == nil || current.TaskType() != "input:collect_user_input")) {
			return runtimeError(ErrInputNotPending, "complete input", err)
		}
		if lookupErr == nil && current.TaskOrdinalToComplete() != task.TaskOrdinalToComplete() {
			return runtimeError(ErrStaleRequest, "complete input", err)
		}
	}
	return runtimeError(ErrStorage, "complete input", err)
}

func readInputError(operation string, err error) error {
	if errors.Is(err, jobdb.ErrJobNotFound) || errors.Is(err, ErrInputNotPending) {
		return runtimeError(ErrInputNotPending, operation, err)
	}
	return runtimeError(ErrStorage, operation, err)
}

func invalidFormError(err error) error {
	return runtimeError(ErrStorage, "decode pending input", fmt.Errorf("invalid persisted form: %w", err))
}

func inputFieldError(field string, err error) error {
	var runtimeErr *RuntimeError
	if errors.As(err, &runtimeErr) {
		copy := *runtimeErr
		copy.Field = field
		return &copy
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ValidationError{Field: field, Message: err.Error(), Cause: err}
}
