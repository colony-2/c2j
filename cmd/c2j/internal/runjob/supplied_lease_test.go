package runjob

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/c2jops"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type suppliedCLI struct {
	backend                *sqlite.Runtime
	client                 *remote.Runtime
	key                    jobdb.JobKey
	opts                   Options
	encoded                []byte
	stdout, stderr         bytes.Buffer
	acquisitions, renewals atomic.Int32
	denyClaims             atomic.Bool
	failRenewAfter         atomic.Int32
}

func newSuppliedCLI(t *testing.T, yaml string, duration time.Duration) *suppliedCLI {
	t.Helper()
	c2jops.Register()
	ctx := context.Background()
	rt, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rt.Close(ctx)) })
	f := &suppliedCLI{backend: rt}
	handler := remote.NewServer(rt)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.denyClaims.Load() && (r.URL.Path == "/v1/jobs/poll" || strings.HasSuffix(r.URL.Path, "/lease")) {
			f.acquisitions.Add(1)
			http.Error(w, "claim forbidden", http.StatusForbidden)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/keepalive") {
			n := f.renewals.Add(1)
			if limit := f.failRenewAfter.Load(); limit > 0 && n > limit {
				http.Error(w, "renewal unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	f.client, err = remote.New(server.URL, server.Client())
	require.NoError(t, err)
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	rec, err := recipe.LoadRecipeFromString([]byte(yaml))
	require.NoError(t, err)
	repo, hash := createGitRepo(t)
	f.key, err = starter.StartRecipeJob(ctx, workflowctl.StartJob{
		TenantId: "tenant", RecipeName: rec.GetMetadata().ID, GitRef: hash,
		JobContext: contextual.JobContext{
			Workflow: contextual.WorkflowContext{ProjectId: "tenant", CellName: "."},
			GitBase:  contextual.GitBaseContext{BaseRepo: repo, BaseRef: hash, ResolvedBaseHash: hash},
		},
	}, jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}, *rec)
	require.NoError(t, err)
	f.opts = Options{JobID: f.key.JobId, JobDBURI: server.URL + "/tenant", CI: true, Stdout: &f.stdout, Stderr: &f.stderr}
	f.claim(t, jobdb.Route{JobType: "recipe"}, duration)
	t.Cleanup(func() { require.Zero(t, f.acquisitions.Load(), "receiver must never acquire a lease") })
	return f
}
func (f *suppliedCLI) claim(t *testing.T, route jobdb.Route, duration time.Duration) {
	t.Helper()
	f.denyClaims.Store(false)
	lease, err := f.client.GetJobLease(context.Background(), jobdb.GetJobLeaseRequest{JobKey: f.key, WorkerID: "dispatcher", Routes: []jobdb.Route{route}, LeaseDuration: duration})
	require.NoError(t, err)
	require.NotNil(t, lease)
	capability, err := remote.ExportLease(lease)
	require.NoError(t, err)
	f.encoded, err = capability.Encode()
	require.NoError(t, err)
	f.denyClaims.Store(true)
}
func (f *suppliedCLI) run(t *testing.T, file bool) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	path := "-"
	f.opts.Stdin = bytes.NewReader(f.encoded)
	if file {
		path = filepath.Join(t.TempDir(), "lease.json")
		require.NoError(t, os.WriteFile(path, f.encoded, 0o600))
	}
	return RunWithLease(ctx, f.opts, path)
}

func TestRunWithLeaseRenewsCompletesAndRetainsArtifacts(t *testing.T) {
	f := newSuppliedCLI(t, `id: supplied
op: command_execution
inputs:
  run: "sleep 1.4; echo saved > result.txt; echo supplied-result"
outputs:
  result: '${{ op.outputs.stdout }}'
`, time.Second)
	require.NoError(t, f.run(t, true), f.stderr.String())
	require.GreaterOrEqual(t, f.renewals.Load(), int32(3))
	info, err := f.backend.GetJob(context.Background(), f.key)
	require.NoError(t, err)
	require.Equal(t, jobdb.JobStatusCompleted, info.Status)
	raw, err := info.Data.GetData()
	require.NoError(t, err)
	require.Contains(t, string(raw), "supplied-result")
	chapters, err := f.backend.ListChapters(context.Background(), jobdb.ListChaptersRequest{JobKey: f.key})
	require.NoError(t, err)
	artifacts := 0
	for _, chapter := range chapters {
		artifacts += len(chapter.Artifacts)
	}
	require.Positive(t, artifacts, "git snapshot artifacts must be persisted through the supplied lease")
	require.ErrorIs(t, f.run(t, false), jobdb.ErrExecutionLeaseLost, "completed capability cannot be reused")
}

func TestRunWithLeaseStopsAtInputAndEnvironmentHandoff(t *testing.T) {
	t.Run("input", func(t *testing.T) {
		f := newSuppliedCLI(t, `id: supplied
op: input
inputs:
  form:
    title: Question
    fields:
      - id: answer
        type: short_answer
        question: Answer?
`, time.Minute)
		err := f.run(t, false)
		var exit exitError
		require.ErrorAs(t, err, &exit)
		require.Equal(t, exitCodeInputRequired, exit.ExitCode())
		require.Contains(t, f.stdout.String(), `"kind":"input_required"`)
		require.NotContains(t, f.stdout.String(), "waiting:")
	})
	t.Run("allocation", func(t *testing.T) {
		f := newSuppliedCLI(t, "id: supplied\nexecution:\n  resources: {memory: 16Gi}\nsequence: []\n", time.Minute)
		small := "2Gi"
		f.opts.ExecutionFlags.Memory = &small
		require.NoError(t, f.run(t, false))
		require.Contains(t, f.stdout.String(), `"kind":"environment_required"`)
		// Re-enter with a freshly dispatched lease. Admission now checks persisted
		// demand under the validated heartbeat before invoking the recipe.
		f.claim(t, jobdb.Route{JobType: "recipe"}, time.Minute)
		f.stdout.Reset()
		require.NoError(t, f.run(t, false))
		require.Contains(t, f.stdout.String(), `"published":false`)
		f.claim(t, jobdb.Route{JobType: "recipe"}, time.Minute)
		large := "16Gi"
		f.opts.ExecutionFlags.Memory = &large
		require.NoError(t, f.run(t, false))
		info, err := f.backend.GetJob(context.Background(), f.key)
		require.NoError(t, err)
		require.Equal(t, jobdb.JobStatusCompleted, info.Status)
	})
}

func TestRunWithLeaseRejectsInvalidAuthorityAndTarget(t *testing.T) {
	for _, test := range []string{"malformed", "token", "job", "tenant", "cancelled", "expired", "initial-renewal"} {
		t.Run(test, func(t *testing.T) {
			duration := time.Minute
			if test == "expired" {
				duration = 40 * time.Millisecond
			}
			f := newSuppliedCLI(t, "id: supplied\nsequence: []\n", duration)
			switch test {
			case "malformed":
				f.encoded = []byte(`{"leaseToken":"do-not-print-this"`)
			case "token":
				var wire map[string]any
				require.NoError(t, json.Unmarshal(f.encoded, &wire))
				wire["leaseToken"] = "do-not-print-this"
				f.encoded, _ = json.Marshal(wire)
			case "job":
				f.opts.JobID = "wrong-job"
			case "tenant":
				f.opts.JobDBURI = strings.TrimSuffix(f.opts.JobDBURI, "/tenant") + "/wrong-tenant"
			case "cancelled":
				require.NoError(t, f.backend.CancelJob(context.Background(), jobdb.CancelJobRequest{JobKey: f.key}))
			case "expired":
				time.Sleep(60 * time.Millisecond)
			case "initial-renewal":
				f.failRenewAfter.Store(1) // Import succeeds; runner validation fails.
			}
			err := f.run(t, false)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "do-not-print-this")
			require.NotContains(t, f.stdout.String()+f.stderr.String(), "do-not-print-this")
			chapters, listErr := f.backend.ListChapters(context.Background(), jobdb.ListChaptersRequest{JobKey: f.key})
			require.NoError(t, listErr)
			require.Len(t, chapters, 1, "only the submitted job chapter should exist")
		})
	}
}

func TestRunWithLeaseHeartbeatFailureStopsRunningCommand(t *testing.T) {
	f := newSuppliedCLI(t, `id: supplied
sequence:
  - id: slow
    op: command_execution
    inputs: {run: 'sleep 5'}
  - id: forbidden
    op: command_execution
    inputs: {run: 'echo must-not-run'}
`, time.Second)
	f.failRenewAfter.Store(2) // Import and initial renewal succeed, heartbeat fails.
	started := time.Now()
	err := f.run(t, false)
	var renewal *jobdb.LeaseRenewalError
	require.ErrorAs(t, err, &renewal)
	var transport *remote.LeaseTransportError
	require.ErrorAs(t, err, &transport)
	require.Less(t, time.Since(started), 3*time.Second)
	require.NotContains(t, f.stdout.String(), "must-not-run")
	info, readErr := f.backend.GetJob(context.Background(), f.key)
	require.NoError(t, readErr)
	require.NotEqual(t, jobdb.JobStatusCompleted, info.Status)
}
