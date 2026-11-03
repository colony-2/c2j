package configinspect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/stretchr/testify/require"
)

func selfReport(t *testing.T, dir string, env map[string]string) (SelfReport, string) {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, RunSelf(context.Background(), SelfOptions{WorkingDir: dir, Stdout: &out, JSONOutput: true, Getenv: func(k string) string { return env[k] }}))
	var report SelfReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	return report, out.String()
}
func TestSelfStandaloneAndPartialContext(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		env                           map[string]string
		execution, permission, reason string
	}{
		{"plain", nil, "absent", "not_applicable", "no_submission_destination"},
		{"connection", map[string]string{defaults.JobDBEnv: "https://example.test/tenant"}, "absent", "not_applicable", "standalone_authority_not_inspected"},
		{"partial job", map[string]string{jobcontext.CurrentJobIDEnv: "job"}, "unknown", "unknown", "incomplete_execution_context"},
		{"partial broker", map[string]string{jobcontext.ChildJobTokenEnv: "private-token"}, "unknown", "unknown", "incomplete_execution_context"},
		{"unreachable", map[string]string{jobcontext.CurrentTenantIDEnv: "tenant", jobcontext.CurrentJobIDEnv: "job", jobcontext.ChildJobEndpointEnv: "http://127.0.0.1:1/v1/child-jobs", jobcontext.ChildJobTokenEnv: "private-token", jobcontext.ChildJobSessionIDEnv: "private-session"}, "present", "unknown", "helper_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, raw := selfReport(t, t.TempDir(), tc.env)
			require.Equal(t, tc.execution, report.Execution.Status)
			require.Equal(t, tc.permission, report.Permissions.Status)
			require.Equal(t, tc.reason, selfinfo.Value(report.Capabilities["jobs.submit"].ReasonCode))
			require.Nil(t, report.Execution.AttemptNumber)
			require.Nil(t, report.Execution.TaskID)
			require.NotContains(t, raw, "private-token")
			require.NotContains(t, raw, "private-session")
			if tc.name == "plain" {
				require.Contains(t, raw, `"repo":null`)
			}
		})
	}
}
func TestSelfRuntimeIdentityAndMismatch(t *testing.T) {
	current := jobcontext.Current{TenantID: "tenant", JobID: "job", GoalID: "goal", InvocationPath: "recipe/probe", InvocationHash: "invocation"}
	server, err := childbroker.Start(context.Background(), childbroker.Options{Current: current, Self: func() selfinfo.Info {
		info := selfinfo.Unknown("")
		info.SetCurrent(current, "runtime_helper")
		info.Execution.TaskID = selfinfo.String("task-123")
		ordinal := int64(9007199254740993)
		info.Execution.ResultOrdinal = &ordinal
		info.Permissions.Status = "known"
		info.Permissions.JobDB = &permissions.JobDB{Jobs: permissions.JobPermissions{Submit: true}}
		info.Lease.Status = "active"
		info.SetCapabilities(true, true, false)
		return info
	}})
	require.NoError(t, err)
	defer server.Close()
	env := jobcontext.MergeProtectedEnv(jobcontext.EnvForCurrent(current), server.Env())
	// Cell resolution failure must not suppress an authenticated task report.
	dir := t.TempDir()
	writeConfig(t, dir, "self: [invalid, configuration]")
	report, _ := selfReport(t, dir, env)
	require.Equal(t, "task-123", selfinfo.Value(report.Execution.TaskID))
	require.Equal(t, int64(9007199254740993), *report.Execution.ResultOrdinal)
	require.Equal(t, "runtime_helper", report.Execution.Source)
	require.Equal(t, "granted", report.Capabilities["jobs.submit"].Permission)
	env[jobcontext.CurrentJobIDEnv] = "different-job"
	report, _ = selfReport(t, dir, env)
	require.Equal(t, "different-job", selfinfo.Value(report.Execution.JobID))
	require.Equal(t, "environment_unverified", report.Execution.Source)
	require.Nil(t, report.Execution.TaskID)
	require.Nil(t, report.Permissions.JobDB)
	require.Equal(t, "execution_identity_mismatch", selfinfo.Value(report.Capabilities["jobs.submit"].ReasonCode))
}
func TestSelfURLRedactionAndStandaloneText(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `self:
  repo: https://user:repo-password@example.test/repo?token=repo-secret
  ref: main
jobdb: https://user:db-password@example.test/tenant?token=db-secret
`)
	_, raw := selfReport(t, dir, nil)
	for _, value := range []string{"repo-password", "repo-secret", "db-password", "db-secret"} {
		require.NotContains(t, raw, value)
	}
	var out bytes.Buffer
	require.NoError(t, RunSelf(context.Background(), SelfOptions{WorkingDir: dir, Stdout: &out, Getenv: func(string) string { return "" }}))
	require.Contains(t, out.String(), "standalone — no current job or task detected")
	require.Contains(t, out.String(), "Submit a new job")
	require.NotContains(t, out.String(), "Task ID:")
	require.NotContains(t, out.String(), "repo-password")
}

type failedSelfWriter struct{}

func (failedSelfWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func TestSelfCancellationAndOutputFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, RunSelf(ctx, SelfOptions{WorkingDir: t.TempDir()}), context.Canceled)
	require.ErrorContains(t, RunSelf(context.Background(), SelfOptions{WorkingDir: t.TempDir(), Stdout: failedSelfWriter{}, Getenv: func(string) string { return "" }}), "write failed")
}
