package workflow

import "fmt"

func NewNonRetryableApplicationError(message string, details ...interface{}) error {

	return &NonRetryableError{fmt.Sprintf(message, details...)}
}

type NonRetryableError struct {
	message string
}

func (e NonRetryableError) Error() string {
	return e.message
}

func (e NonRetryableError) NonRetryable() bool {
	return true
}

type RetryableErrror struct {
	message string
}

func (e RetryableErrror) Error() string {
	return e.message
}

func (e RetryableErrror) NonRetryable() bool {
	return false
}

func NewApplicationError(message string, details ...interface{}) error {
	return &RetryableErrror{fmt.Sprintf(message, details...)}
}
