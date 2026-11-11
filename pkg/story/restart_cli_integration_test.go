package story_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/jobrestart"
	restartservice "github.com/colony-2/c2j/pkg/jobrestart/service"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type restartReplayCounter struct {
	wf.Engine
	replays atomic.Int32
}

func (e *restartReplayCounter) ReplayJobRun(ctx context.Context, q wf.ReplayRunRequest) (jobdb.JobData, error) {
	e.replays.Add(1)
	return e.Engine.ReplayJobRun(ctx, q)
}

// Exercises the built executable, authorized helper transport, and shared service
// over both SQLite and HTTP. No external job or server participates.
func TestRestartCLIEndToEnd(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "c2j")
	build := exec.Command("go", "build", "-o", binary, "./cmd/c2j")
	build.Dir = "../.."
	raw, err := build.CombinedOutput()
	require.NoError(t, err, string(raw))
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newRestartFixture(t, backend, restartSequenceRecipe)
			count := &restartReplayCounter{Engine: f.engine}
			svc := &restartservice.Service{Engine: count, Tenant: f.key.TenantId}
			svc.Submit = func(ctx context.Context, key jobdb.JobKey, scope string, patch *task.ContextPatch, opts starter.RestartOptions) (jobref.Ref, error) {
				return starter.RestartRecipeJob(ctx, jobdbschema.WorkflowEngine{Engine: f.engine, Registry: f.runtime.(jobdb.JobSchemaRegistry)}, key, scope, patch, opts)
			}
			current := jobcontext.Current{TenantID: f.key.TenantId, JobID: f.key.JobId, GoalID: "current-goal"}
			server, err := childbroker.Start(jobrestart.WithAPI(f.ctx, svc), childbroker.Options{Current: current, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
			require.NoError(t, err)
			defer server.Close()
			env := []string{}
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "C2J_CURRENT_") && !strings.HasPrefix(entry, "C2J_CHILD_JOB_") && !strings.HasPrefix(entry, "C2J_JOBDB=") {
					env = append(env, entry)
				}
			}
			for k, v := range jobcontext.EnvForCurrent(current) {
				env = append(env, k+"="+v)
			}
			for k, v := range server.Env() {
				env = append(env, k+"="+v)
			}
			env = append(env, "C2J_JOBDB=invalid://ambient")
			cli := func(args ...string) ([]byte, error) {
				cmd := exec.CommandContext(f.ctx, binary, args...)
				cmd.Env = env
				return cmd.CombinedOutput()
			}
			before, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: f.key, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			raw, err := cli("job", "restart-points", "self", "--json", "--jobdb", "invalid://explicit")
			require.NoError(t, err, string(raw))
			var points jobrestart.Points
			require.NoError(t, json.Unmarshal(raw, &points))
			require.Greater(t, len(points.RestartScopes), 4)
			require.Equal(t, f.key.TenantId, points.ProjectID)
			require.Zero(t, count.replays.Load(), "discovery never replays")
			st := f.story(t, f.key)
			ops := storyNodes(st.Root, "op", restartProbeOp)
			scope := ops[2].RestartScopeID
			raw, err = cli("job", "restart-context", "self", "--scope", scope)
			require.NoError(t, err, string(raw))
			var snapshot jobrestart.RestartContext
			require.NoError(t, json.Unmarshal(raw, &snapshot))
			require.Equal(t, scope, snapshot.ScopeID)
			require.Equal(t, f.key.TenantId, snapshot.ProjectID)
			require.NotEmpty(t, snapshot.Job)
			for _, key := range []string{"inputs", "sequence", "states", "vars", "outputs", "transition", "failure", "scope", "context", "item", "index"} {
				require.Contains(t, snapshot.Visible, key)
			}
			require.Equal(t, "[REDACTED]", snapshot.Visible["sequence"].(map[string]any)["inner"].(map[string]any)["outputs"].(map[string]any)["api_key"])
			file := filepath.Join(t.TempDir(), "snapshot.json")
			stdout, err := cli("job", "restart-context", "self", "--scope", scope, "--output", file)
			require.NoError(t, err, string(stdout))
			require.Empty(t, stdout)
			stored, err := os.ReadFile(file)
			require.NoError(t, err)
			var fileSnapshot jobrestart.RestartContext
			require.NoError(t, json.Unmarshal(stored, &fileSnapshot))
			// Replay creates fresh scope time/identity; all other snapshot values match.
			require.NotEmpty(t, fileSnapshot.Visible["scope"])
			require.NotEmpty(t, snapshot.Visible["scope"])
			fileSnapshot.Visible["scope"] = snapshot.Visible["scope"]
			require.Equal(t, snapshot, fileSnapshot)
			require.Equal(t, int32(4), f.task.calls.Load(), "inspection performs no operations")
			after, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: f.key, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			require.Equal(t, before, after)
			patchFile := filepath.Join(t.TempDir(), "patch.json")
			// JP-20: even the exact ID/payload of an accepted pre-upgrade
			// request must fail at public ingress, before identity lookup.
			legacyBody := json.RawMessage(`{"job":{"git":{"author":"Legacy Author"}}}`)
			legacyJob := jobdb.SubmitRestartJob{Cell: "test", PriorJobKey: f.key, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: scope, Payload: legacyBody}}
			require.NoError(t, jobdbschema.SetSubmitRestartJobSchema(&legacyJob))
			require.NoError(t, jobdbschema.Register(f.ctx, f.runtime.(jobdb.JobSchemaRegistry), f.key.TenantId))
			acceptedLegacy, err := f.runtime.SubmitRestartJob(f.ctx, jobdb.SubmitRestartJobRequest{RequestID: "accepted-legacy-cli", Job: legacyJob})
			require.NoError(t, err)
			legacyBefore, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: acceptedLegacy.JobKey, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(patchFile, legacyBody, 0600))
			legacyRetry, err := cli("restart", "self", "--scope", scope, "--request-id", "accepted-legacy-cli", "--goal", "new", "--patch-file", patchFile)
			require.Error(t, err)
			require.Contains(t, string(legacyRetry), "operation array")
			legacyAfter, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: acceptedLegacy.JobKey, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			require.Equal(t, legacyBefore, legacyAfter, "rejection must not alter the accepted historical continuation")
			require.NoError(t, os.WriteFile(patchFile, []byte(`[{"op":"add","path":"/context/git/author","value":"CLI Author"},{"op":"replace","path":"/sequence/inner/outputs/value","value":"cli-patched"},{"op":"remove","path":"/sequence/inner/outputs/unchanged"},{"op":"add","path":"/sequence/inner/outputs/array","value":[1,2]}]`), 0600))
			replays := count.replays.Load()
			args := []string{"restart", "self", "--scope", scope, "--request-id", "cli-retry", "--goal", "new", "--patch-file", patchFile}
			raw, err = cli(args...)
			require.NoError(t, err, string(raw))
			var ref jobref.Ref
			require.NoError(t, json.Unmarshal(raw, &ref))
			require.NoError(t, ref.Validate())
			require.Equal(t, "test", ref.Cell)
			retry, err := cli(args...)
			require.NoError(t, err, string(retry))
			require.JSONEq(t, string(raw), string(retry))
			require.Equal(t, replays, count.replays.Load(), "submission reads eligibility but does not inspect context")
			require.NoError(t, os.WriteFile(patchFile, []byte(`[]`), 0600))
			conflict, err := cli(args...)
			require.Error(t, err, string(conflict))
			f.run(t, ref.Key())
			require.Equal(t, int32(6), f.task.calls.Load(), "prefix tasks reused")
			restarted := f.story(t, ref.Key())
			require.Equal(t, "cli-patched", restarted.Root.Output.(map[string]any)["value"])
			after, err = f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: f.key, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			require.Equal(t, before, after, "source history and status unchanged")

			// A later restart retains an earlier boundary patch and its completed prefix.
			lastScope := storyNodes(restarted.Root, "op", restartProbeOp)[3].RestartScopeID
			require.Equal(t, "CLI Author", f.inspectScope(t, ref.Key(), lastScope).Job["git"].(map[string]any)["author"])
			raw, err = cli("restart", ref.JobID, "--scope", lastScope, "--request-id", "retain-earlier", "--goal", "new")
			require.NoError(t, err, string(raw))
			var retained jobref.Ref
			require.NoError(t, json.Unmarshal(raw, &retained))
			f.run(t, retained.Key())
			require.Equal(t, int32(7), f.task.calls.Load())
			require.Equal(t, "cli-patched", f.story(t, retained.Key()).Root.Output.(map[string]any)["value"])
			// A new patch at the same boundary replaces its old payload.
			require.NoError(t, os.WriteFile(patchFile, []byte(`[{"op":"replace","path":"/sequence/inner/outputs/value","value":"replacement"}]`), 0600))
			raw, err = cli("restart", ref.JobID, "--scope", scope, "--request-id", "replace-patch", "--goal", "new", "--patch-file", patchFile)
			require.NoError(t, err, string(raw))
			var replaced jobref.Ref
			require.NoError(t, json.Unmarshal(raw, &replaced))
			f.run(t, replaced.Key())
			require.Equal(t, "replacement", f.story(t, replaced.Key()).Root.Output.(map[string]any)["value"])
			// Omitting a patch at the same boundary restores its original base.
			args = []string{"restart", replaced.JobID, "--scope", scope, "--request-id", "cli-unpatched", "--goal", "new"}
			raw, err = cli(args...)
			require.NoError(t, err, string(raw))
			var unpatched jobref.Ref
			require.NoError(t, json.Unmarshal(raw, &unpatched))
			f.run(t, unpatched.Key())
			require.Equal(t, "original", f.story(t, unpatched.Key()).Root.Output.(map[string]any)["value"])
			// Explicit chapter zero is accepted, while all nonempty patches are refused.
			for _, body := range []string{`[{"op":"add","path":"/context/git/author","value":"x"}]`, `[{"op":"test","path":"/sequence/inner/outputs/value","value":"original"}]`} {
				require.NoError(t, os.WriteFile(patchFile, []byte(body), 0600))
				raw, err = cli("restart", "self", "--chapter", "0", "--request-id", "invalid-submission", "--patch-file", patchFile)
				require.Error(t, err, string(raw))
				require.Contains(t, string(raw), "submission restart does not support")
			}
			require.NoError(t, os.WriteFile(patchFile, []byte(`[]`), 0600))
			raw, err = cli("restart", "self", "--chapter", "0", "--request-id", "empty-submission", "--goal", "new", "--patch-file", patchFile)
			require.NoError(t, err, string(raw))
			// Ordinary caller routing uses the configured tenant URL without a helper.
			if backend == "sqlite" {
				plain := newRestartFixture(t, "sqlite", "id: simple\nsequence:\n - id: nested\n   sequence: []\n")
				plainScope := plain.story(t, plain.key).RestartScopes[1].ScopeID
				httpServer := httptest.NewServer(celltest.Handler(remote.NewServer(plain.runtime)))
				defer httpServer.Close()
				cmd := exec.CommandContext(f.ctx, binary, "job", "restart-points", plain.key.JobId, "--json", "--jobdb", httpServer.URL+"/"+f.key.TenantId)
				for _, entry := range env {
					if !strings.HasPrefix(entry, "C2J_CURRENT_") && !strings.HasPrefix(entry, "C2J_CHILD_JOB_") {
						cmd.Env = append(cmd.Env, entry)
					}
				}
				raw, err = cmd.CombinedOutput()
				require.NoError(t, err, string(raw))
				require.Contains(t, string(raw), `"project_id":"restart-tests"`)
				cmd = exec.CommandContext(f.ctx, binary, "job", "restart-context", plain.key.JobId, "--scope", plainScope, "--jobdb", httpServer.URL+"/"+f.key.TenantId)
				for _, entry := range env {
					if !strings.HasPrefix(entry, "C2J_CURRENT_") && !strings.HasPrefix(entry, "C2J_CHILD_JOB_") {
						cmd.Env = append(cmd.Env, entry)
					}
				}
				raw, err = cmd.CombinedOutput()
				require.NoError(t, err, string(raw))
				require.Contains(t, string(raw), `"scope_id"`)

			}
		})
	}
}
