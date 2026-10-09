package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Execute the actual entry point in a subprocess to exercise flag parsing,
// JobDB, error wrapping, stderr formatting, and the process exit status.
func TestLeaseDiagnosticProcess(t *testing.T) {
	if os.Getenv("C2J_TEST_LEASE_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"c2j"}, os.Args[i+1:]...)
			main()
			return
		}
	}
	t.Fatal("missing subprocess arguments")
}

func TestWithLeaseCLIErrorOutput(t *testing.T) {
	const token = "secret-lease-token"
	for _, tt := range []struct {
		name, detail, status string
		hint                 bool
	}{
		{"refused", "connection refused", "", true},
		{"decode", "invalid renewal response", "HTTP status 200", false},
		{"read", "response body could not be read", "HTTP status 200", false},
		{"HTTP", "HTTP request rejected", "HTTP status 503", false},
		{"authority", "HTTP request rejected", "HTTP status 403", false},
		{"TLS", "TLS negotiation failed", "", false},
		{"credentials", "must not include user info", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/keepalive") || r.Header.Get("X-JobDB-Lease-Token") != token {
					t.Error("unexpected request or missing supplied token")
				}
				w.Header().Set("Content-Type", "application/json")
				switch tt.name {
				case "HTTP":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "authority":
					w.WriteHeader(http.StatusForbidden)
				case "read":
					w.Header().Set("Content-Length", "1000")
				}
				fmt.Fprintf(w, `{"lease": %q}`, token)
			}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			if tt.name == "TLS" {
				server.StartTLS()
			} else {
				server.Start()
			}
			t.Cleanup(server.Close)
			target := server.URL + "/tenant"
			if tt.name == "refused" {
				server.Close()
			}
			if tt.name == "credentials" {
				target = "http://secret-user:secret-password@localhost/tenant?token=secret-query"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLeaseDiagnosticProcess$", "--",
				"run", "with-lease", "--ci", "--job-id", "job", "--lease-file", "-", "--jobdb", target)
			command.Env = append(os.Environ(), "C2J_TEST_LEASE_PROCESS=1")
			command.Dir = t.TempDir()
			command.Stdin = strings.NewReader(`{"version":1,"tenantId":"tenant","jobId":"job","leaseId":"lease","leaseToken":"` + token + `"}`)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			require.Equal(t, 1, exit.ExitCode())
			require.Empty(t, stdout.String())
			output := stderr.String()
			require.True(t, strings.HasPrefix(output, "Error: "), output)
			require.Contains(t, output, tt.detail)
			if tt.status != "" {
				require.Contains(t, output, tt.status)
			}
			if tt.name != "credentials" {
				require.Contains(t, output, target)
				require.Contains(t, output, "job execution has not started")
			}
			require.Equal(t, tt.hint, strings.Contains(output, "If running inside Docker"))
			require.NotContains(t, output, "HTTP status 0")
			require.NotContains(t, output, "secret-")
		})
	}
}
