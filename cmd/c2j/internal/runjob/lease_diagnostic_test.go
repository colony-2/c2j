package runjob

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/stretchr/testify/require"
)

// JobDB tests the transport matrix; these tests cover c2j's presentation and
// hint policy using the public diagnostic metadata.
func TestSuppliedLeaseValidationDiagnostic(t *testing.T) {
	refused := &remote.LeaseTransportError{
		Operation: "renew", Phase: remote.LeasePhaseExchange, Category: remote.LeaseFailureRefused,
		Err: fmt.Errorf("secret-error: %w", syscall.ECONNREFUSED),
	}

	for _, tt := range []struct {
		name, target, detail string
		cause                error
		hint                 bool
	}{
		{"refused", "http://localhost:9047/c2", "connection refused", refused, true},
		{"IPv4 loopback", "http://127.2.3.4:9047/c2", "connection refused", refused, true},
		{"IPv6 loopback", "http://[::1]:9047/c2", "connection refused", refused, true},
		{"localhost trailing dot", "http://LOCALHOST.:9047/c2", "connection refused", refused, true},
		{"remote", "http://jobdb.example/c2", "connection refused", refused, false},
		{"not localhost", "http://localhost.example/c2", "connection refused", refused, false},
		{"unreachable", "http://localhost/c2", "destination unreachable", &remote.LeaseTransportError{Phase: remote.LeasePhaseExchange, Category: remote.LeaseFailureUnreachable}, true},
		{"timeout has ambiguous phase", "http://localhost/c2", "request timed out", &remote.LeaseTransportError{Phase: remote.LeasePhaseExchange, Category: remote.LeaseFailureTimeout}, false},
		{"response received", "http://localhost/c2", "connection refused", &remote.LeaseTransportError{Phase: remote.LeasePhaseExchange, Category: remote.LeaseFailureRefused, ResponseReceived: true, StatusCode: 302}, false},
		{"decode", "http://localhost/c2", "invalid renewal response", &remote.LeaseTransportError{Phase: remote.LeasePhaseDecode, Category: remote.LeaseFailureDecode, ResponseReceived: true, StatusCode: 200, Err: errors.New("secret-body")}, false},
		{"HTTP authority rejection retains status", "http://localhost/c2", "HTTP status 403", &remote.LeaseTransportError{Phase: remote.LeasePhaseExchange, Category: remote.LeaseFailureHTTP, ResponseReceived: true, StatusCode: 403, Err: jobdb.ErrExecutionLeaseLost}, false},
		{"unsupported response retains status", "http://localhost/c2", "HTTP status 200", &remote.LeaseTransportError{Phase: remote.LeasePhaseSnapshot, Category: remote.LeaseFailureUnsupported, ResponseReceived: true, StatusCode: 200, Err: jobdb.ErrLeaseRenewalUnsupported}, false},
		{"unknown", "http://localhost/c2", "request failed", &remote.LeaseTransportError{Err: errors.New("secret-lease-token Authorization: secret-auth")}, false},
		{"non-transport", "http://localhost/c2", "lease validation failed", errors.New("secret-error"), false},
		{"lost", "http://localhost/c2", "lease is invalid, expired, or no longer held", jobdb.ErrExecutionLeaseLost, false},
		{"unsupported", "http://localhost/c2", "does not support", jobdb.ErrLeaseRenewalUnsupported, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := newSuppliedLeaseValidationError(tt.target, fmt.Errorf("secret-wrapper: %w", tt.cause))
			require.ErrorIs(t, err, tt.cause)
			for _, format := range []string{"%s", "%v", "%+v", "%#v"} {
				message := fmt.Sprintf(format, err)
				require.Contains(t, message, tt.target)
				require.Contains(t, message, tt.detail)
				require.Contains(t, message, "job execution has not started")
				require.NotContains(t, message, "HTTP status 0")
				require.NotContains(t, message, "secret-")
				require.Equal(t, tt.hint, strings.Contains(message, "If running inside Docker"))
			}
		})
	}
	err := newSuppliedLeaseValidationError("http://localhost/c2", refused)
	var transport *remote.LeaseTransportError
	require.ErrorAs(t, err, &transport)
	require.Same(t, refused, transport)
	require.ErrorIs(t, err, syscall.ECONNREFUSED)
}

func TestSuppliedLeaseValidationDestinationRedaction(t *testing.T) {
	for _, target := range []string{
		"http://secret-user:secret-password@localhost:9047/c2?token=secret-query&unknown=secret-value#secret-fragment",
		"http://secret-user:secret-password@localhost:bad/c2",
	} {
		err := newSuppliedLeaseValidationError(target, &remote.LeaseTransportError{Err: errors.New("secret-error")})
		require.NotContains(t, err.Error(), "secret-")
		if target == "http://secret-user:secret-password@localhost:bad/c2" {
			require.Contains(t, err.Error(), "configured JobDB destination")
		} else {
			require.Contains(t, err.Error(), "http://localhost:9047/c2")
		}
	}
}
