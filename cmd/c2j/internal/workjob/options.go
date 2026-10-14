package workjob

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	exitCodeFailure        = 1
	exitCodeInvalidOptions = 2
)

func validateJobDBRuntimeURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("parse JobDB runtime URL: %w", err)
	}
	switch parsed.Scheme {
	case "http", "https", "embed":
		return nil
	default:
		return fmt.Errorf("unsupported JobDB runtime URL %q", raw)
	}
}

type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit %d", e.code)
	}
	return e.err.Error()
}

func (e exitError) Unwrap() error { return e.err }
func (e exitError) ExitCode() int { return e.code }
