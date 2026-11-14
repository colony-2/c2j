package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/selfinfo"
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
			command.Stdin = strings.NewReader(`{"version":2,"tenantId":"tenant","jobId":"job","leaseId":"lease","leaseToken":"` + token + `"}`)
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

type dagProcessAPI struct{ partial, absent bool }

func (a dagProcessAPI) Inspect(_ context.Context, q jobinspect.Request) (*jobinspect.Response, error) {
	if a.absent {
		return nil, fmt.Errorf("entry unavailable")
	}
	if q.Operation == "family-children" && a.partial {
		return nil, fmt.Errorf("children unavailable")
	}
	p := &jobinspect.FamilyPage{Version: 1, Nodes: []jobinspect.FamilyNode{}}
	if q.Operation == "family-node" {
		p.Nodes = append(p.Nodes, jobinspect.FamilyNode{ID: q.JobID, Tenant: "tenant", Status: "READY"})
	}
	return &jobinspect.Response{Version: 1, Family: p}, nil
}
func (dagProcessAPI) Download(context.Context, jobinspect.Request) (io.ReadCloser, error) {
	panic("unexpected download")
}
func TestDAGCLIProcessExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		api  dagProcessAPI
		code int
	}{{"complete", dagProcessAPI{}, 0}, {"partial", dagProcessAPI{partial: true}, 2}, {"absent", dagProcessAPI{absent: true}, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			current := jobcontext.Current{TenantID: "tenant", JobID: "selected", GoalID: "goal"}
			server, err := childbroker.Start(jobinspect.WithAPI(context.Background(), tc.api), childbroker.Options{Current: current, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
			require.NoError(t, err)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLeaseDiagnosticProcess$", "--", "job", "dag", "self", "--json", "--jobdb", "invalid://ambient")
			var env []string
			for _, kv := range os.Environ() {
				k, _, _ := strings.Cut(kv, "=")
				if !strings.HasPrefix(k, "C2J_CURRENT_") && !strings.HasPrefix(k, "C2J_CHILD_JOB_") {
					env = append(env, kv)
				}
			}
			for k, v := range jobcontext.EnvForCurrent(current) {
				env = append(env, k+"="+v)
			}
			for k, v := range server.Env() {
				env = append(env, k+"="+v)
			}
			command.Env = append(env, "C2J_TEST_LEASE_PROCESS=1")
			command.Dir = t.TempDir()
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err = command.Run()
			if tc.code == 0 {
				require.NoError(t, err)
				require.Empty(t, stderr.String())
			} else {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				require.Equal(t, tc.code, exit.ExitCode())
				require.Contains(t, stderr.String(), "Error:")
			}
			if tc.code == 1 {
				require.Empty(t, stdout.String())
			} else {
				var d jobinspect.DAG
				require.NoError(t, json.Unmarshal(stdout.Bytes(), &d))
				require.Equal(t, tc.code == 0, d.Coverage.Complete)
				require.NotNil(t, d.Nodes)
				require.NotNil(t, d.Edges)
				require.NotNil(t, d.Coverage.Anomalies)
			}
		})
	}
}
