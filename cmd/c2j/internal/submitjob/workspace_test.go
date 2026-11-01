package submitjob

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/runjob"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/c2j/pkg/workspacesubmit"
	"github.com/colony-2/jobdb/pkg/jobdb"
	remoteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

const workspaceSubmitRecipe = `id: workspace_submit_test
version: "1"
input_schema:
 prompt: {type: string}
sequence:
 - id: read
   op: command_execution
   inputs:
    working_directory: "{{ context.environment.op.worktree_path }}"
    run: "cat value.txt"
outputs:
 value: "{{ sequence.read.outputs.stdout }}"
`

func TestSubmitWorkspaceNowRunsOnFreshWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, _ := createGitRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".shai"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".shai", "config.yaml"), []byte("self:\n  repo: "+root+"\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "value.txt"), []byte("captured"), 0644))
	recipePath := filepath.Join(t.TempDir(), "recipe.yaml")
	require.NoError(t, os.WriteFile(recipePath, []byte(workspaceSubmitRecipe), 0644))
	rt := toyruntime.New()
	server := httptest.NewServer(remoteruntime.NewServer(rt))
	defer server.Close()
	uri := testJobDBURI(server.URL, "workspace-submit")
	var stdout bytes.Buffer
	require.NoError(t, Run(ctx, Options{JobDBURI: uri, WorkingDir: root, RecipeFile: recipePath, Prompt: "read", FromWorkspaceNow: true, JSONOutput: true, Stdout: &stdout}))
	var submitted submitResult
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &submitted))
	require.NoError(t, os.WriteFile(filepath.Join(root, "value.txt"), []byte("after submission"), 0644))
	var logs bytes.Buffer
	require.NoError(t, runjob.Run(ctx, runjob.Options{JobDBURI: uri, JobID: submitted.JobID, WaitTimeout: 15 * time.Second, PollInterval: 10 * time.Millisecond, InputMode: "fail", Stdout: &logs, Stderr: &logs}), logs.String())
	remote, err := remoteruntime.New(server.URL, server.Client())
	require.NoError(t, err)
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(remote).BuildEngine()
	require.NoError(t, err)
	run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: jobdb.JobKey{TenantId: submitted.TenantID, JobId: submitted.JobID}, IncludeOutputs: true})
	require.NoError(t, err)
	output, err := run.GetOutput(engine, submitted.TenantID)
	require.NoError(t, err)
	raw, err := output.GetData()
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "captured", got["value"])
}
func TestSubmitWorkspaceFlagsThroughBroker(t *testing.T) {
	ctx := context.Background()
	root, base := createGitRepo(t)
	recipePath := filepath.Join(t.TempDir(), "recipe.yaml")
	require.NoError(t, os.WriteFile(recipePath, []byte(workspaceSubmitRecipe), 0644))
	current := jobcontext.Current{TenantID: "0", GoalID: "parent-goal", JobID: "parent", InvocationHash: "invocation"}
	capture := &submitjobCaptureSubmitter{}
	broker, err := childbroker.Start(ctx, childbroker.Options{Current: current, Submitter: capture, Workspace: &workspacesubmit.Source{Git: gitstate.GlobalGitTaskContext{BaseRepo: root, BaseRef: "main", ResolvedBaseHash: base, CellName: "active"}, Worktree: root}})
	require.NoError(t, err)
	defer broker.Close()
	for k, v := range jobcontext.EnvForCurrent(current) {
		t.Setenv(k, v)
	}
	for k, v := range broker.Env() {
		t.Setenv(k, v)
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "value.txt"), []byte("during task"), 0644))
	for _, now := range []bool{false, true} {
		var stdout bytes.Buffer
		require.NoError(t, Run(ctx, Options{JobDBURI: "embed:///", WorkingDir: t.TempDir(), RecipeFile: recipePath, Prompt: "read", FromWorkspace: !now, FromWorkspaceNow: now, JSONOutput: true, Stdout: &stdout}))
		raw, err := capture.last.Data.GetData()
		require.NoError(t, err)
		var start workflowctl.StartJob
		require.NoError(t, json.Unmarshal(raw, &start))
		require.Equal(t, "active", start.JobContext.Workflow.CellName)
		require.Equal(t, root, start.JobContext.GitBase.BaseRepo)
		require.Equal(t, "parent", start.Parent.JobID)
		if now {
			require.NotEqual(t, base, start.GitRef)
			require.Equal(t, workspacesubmit.ArtifactName, start.JobContext.RestoreArtifactName)
		} else {
			require.Equal(t, base, start.GitRef)
			require.Empty(t, start.JobContext.RestoreArtifactName)
		}
		var meta starter.JobMetadata
		require.NoError(t, json.Unmarshal(capture.last.Metadata, &meta))
		require.Equal(t, "invocation", meta.ParentInvocationHash)
	}
	before := capture.calls
	err = Run(ctx, Options{JobDBURI: "embed:///", WorkingDir: root, RecipeFile: recipePath, Prompt: "read", FromWorkspace: true, Cell: "https://example.com/other.git"})
	require.ErrorContains(t, err, "does not match")
	require.Equal(t, before, capture.calls)
}
func TestSubmitWorkspaceFlagValidation(t *testing.T) {
	require.ErrorContains(t, (Options{FromWorkspace: true, FromWorkspaceNow: true}).Validate(), "mutually exclusive")
	err := Run(context.Background(), Options{JobDBURI: "embed:///", WorkingDir: t.TempDir(), Prompt: "read", FromWorkspace: true})
	require.ErrorContains(t, err, "use --from-workspace-now")
}

func TestWorkspaceSubmitRejectsOlderBrokerBeforePosting(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	current := jobcontext.Current{TenantID: "0", GoalID: "goal", JobID: "parent"}
	for k, v := range jobcontext.EnvForCurrent(current) {
		t.Setenv(k, v)
	}
	for k, v := range jobcontext.EnvForChildJobBroker(jobcontext.ChildJobBroker{Endpoint: server.URL + "/v1/child-jobs", Token: "token", SessionID: "session"}) {
		t.Setenv(k, v)
	}
	err := Run(context.Background(), Options{JobDBURI: "embed:///", WorkingDir: t.TempDir(), Prompt: "read", FromWorkspace: true})
	require.ErrorContains(t, err, "requires a worker with workspace support")
	require.Zero(t, posts.Load())
}
