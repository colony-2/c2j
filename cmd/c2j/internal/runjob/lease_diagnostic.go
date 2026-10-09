package runjob

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
)

// Keep the original chain for callers, but never interpolate its raw text:
// URLs, response errors, and custom transports may contain credentials.
type suppliedLeaseValidationError struct {
	message string
	cause   error
}

func (e *suppliedLeaseValidationError) Error() string    { return e.message }
func (e *suppliedLeaseValidationError) GoString() string { return e.message }
func (e *suppliedLeaseValidationError) Unwrap() error    { return e.cause }

func newSuppliedLeaseValidationError(target string, err error) error {
	destination := "configured JobDB destination"
	loopback := false
	if u, parseErr := url.Parse(target); parseErr == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		// Drop all user info, query values and fragments, including unknown
		// credential names. URL.Redacted alone only hides the password.
		destination = (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
		host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
		loopback = host == "localhost" || net.ParseIP(host).IsLoopback()
	}
	detail, connectivity := suppliedLeaseFailureDetail(err)
	message := fmt.Sprintf("cannot validate supplied lease at %s: %s; job execution has not started", destination, detail)
	if loopback && connectivity {
		message += ". If running inside Docker, localhost normally refers to this container. Use a JobDB address reachable from the container."
	}
	return &suppliedLeaseValidationError{message: message, cause: err}
}

// JobDB owns transport classification and safe rendering. c2j adds invocation
// context and only uses typed metadata to decide whether networking advice fits.
func suppliedLeaseFailureDetail(err error) (detail string, connectivity bool) {
	var transport *remote.LeaseTransportError
	if errors.As(err, &transport) {
		connectivity = transport.Phase == remote.LeasePhaseExchange && !transport.ResponseReceived &&
			(transport.Category == remote.LeaseFailureRefused || transport.Category == remote.LeaseFailureUnreachable)
		return transport.SafeMessage(), connectivity
	}
	if errors.Is(err, jobdb.ErrExecutionLeaseLost) {
		return "lease is invalid, expired, or no longer held", false
	}
	if errors.Is(err, jobdb.ErrLeaseRenewalUnsupported) {
		return "JobDB does not support the required lease renewal response", false
	}
	return "lease validation failed", false
}
