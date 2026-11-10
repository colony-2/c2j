package submitjob

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

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/workspacesubmit"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/colony-2/jobdb/pkg/jobdb/runtimetest"
	workflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestSelectedCLILostResponseRetainsExactRequest(t *testing.T) {
	ctx := celltest.Context(runtimetest.PrimaryGoalContext(context.Background()))
	rt := toy.New()
	root, _ := createGitRepo(t)
	celltest.Create(t, rt, "selected", "app", celltest.Repo(root))
	source, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "selected", JobID: "source", Cell: "app", CellRepo: celltest.Repo(root), JobType: "work", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	recipeFile := filepath.Join(t.TempDir(), "recipe.yaml")
	require.NoError(t, os.WriteFile(recipeFile, []byte(workspaceSubmitRecipe), 0600))
	requestFile := filepath.Join(t.TempDir(), "request.json")
	var publications atomic.Int32
	h := remote.NewServer(rt)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(runtimetest.PrimaryGoalContext(r.Context()))
		if strings.HasSuffix(r.URL.Path, "/primary-goal") {
			_, e := os.Stat(requestFile)
			require.NoError(t, e, "descriptor must exist before publication")
			recorder := httptest.NewRecorder()
			h.ServeHTTP(recorder, r)
			if recorder.Code == 200 && publications.Add(1) == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"broken":`))
				return
			}
			for k, v := range recorder.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(recorder.Code)
			w.Write(recorder.Body.Bytes())
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	uri := testJobDBURI(server.URL, "selected")
	opts := Options{JobDBURI: uri, WorkingDir: root, Cell: "app", RecipeFile: recipeFile, Prompt: "new instructions", PromptSet: true, GoalID: source.GoalID, JobID: "dest", RequestFile: requestFile, JSONOutput: true, Stdout: &bytes.Buffer{}}
	firstErr := Run(ctx, opts)
	require.Error(t, firstErr)
	t.Logf("first result: %v; committed responses: %d", firstErr, publications.Load())
	_, checkErr := rt.GetJob(ctx, jobdb.JobKey{TenantId: "selected", JobId: "dest"})
	require.NoError(t, checkErr)
	raw, err := os.ReadFile(requestFile)
	require.NoError(t, err)
	var saved retainedSubmission
	require.NoError(t, json.Unmarshal(raw, &saved))
	require.Equal(t, "dest", saved.Descriptor.Job.JobID)
	require.Equal(t, source.GoalID, saved.Descriptor.Job.GoalID)
	var prepared map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(saved.Descriptor.Input, &prepared))
	require.NotContains(t, prepared, "goal_id")
	stat, err := os.Stat(requestFile)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), stat.Mode().Perm())
	require.NoError(t, os.Remove(recipeFile))
	require.NoError(t, os.WriteFile(filepath.Join(root, "value.txt"), []byte("changed"), 0600))
	_, err = rt.FailGoal(ctx, jobdb.FailGoalRequest{GoalKey: jobdb.GoalKey{TenantId: "selected", GoalID: source.GoalID}, Message: "closed"})
	require.NoError(t, err)
	var stdout bytes.Buffer
	require.NoError(t, Run(ctx, Options{JobDBURI: uri, WorkingDir: t.TempDir(), ReplayFile: requestFile, JSONOutput: true, Stdout: &stdout}))
	var ref submitResult
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &ref))
	require.Equal(t, source.CellID, ref.CellID)
	require.Equal(t, source.CellRepo, ref.CellRepo)
	require.Equal(t, source.Cell, ref.Cell)
	require.Equal(t, "selected", ref.TenantID)
	require.Equal(t, source.GoalID, ref.GoalID)
	require.Equal(t, "dest", ref.JobID)
	after, err := os.ReadFile(requestFile)
	require.NoError(t, err)
	require.Equal(t, raw, after)
	changed := saved
	changed.Descriptor.Job.Metadata = json.RawMessage(`{"different":true}`)
	changedFile := filepath.Join(t.TempDir(), "changed.json")
	require.NoError(t, saveRetained(changedFile, changed))
	require.ErrorIs(t, Run(ctx, Options{JobDBURI: uri, ReplayFile: changedFile, Stdout: &bytes.Buffer{}}), jobdb.ErrExistingJobMismatch)
}
func TestSelectedCLIWorkspaceRoutes(t *testing.T) {
	for _, mode := range []string{"incoming", "current", "direct-current"} {
		t.Run(mode, func(t *testing.T) {
			ctx := celltest.Context(runtimetest.PrimaryGoalContext(context.Background()))
			root, base := createGitRepo(t)
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".c2"), 0755))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".c2", "config.yaml"), []byte("self:\n  cell: active\n  repo: "+celltest.Repo(root)+"\n"), 0600))
			rt := toy.New()
			celltest.Create(t, rt, "0", "active", celltest.Repo(root))
			source, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "0", JobID: "source", Cell: "active", CellRepo: celltest.Repo(root), JobType: "work", Data: jobdb.NewTaskDataOrPanic(nil)}})
			require.NoError(t, err)
			recipePath := filepath.Join(t.TempDir(), "recipe.yaml")
			require.NoError(t, os.WriteFile(recipePath, []byte(workspaceSubmitRecipe), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(root, "value.txt"), []byte("during task"), 0600))
			opts := Options{JobDBURI: "embed:///", WorkingDir: root, RecipeFile: recipePath, Prompt: "read", GoalID: source.GoalID, JobID: "dest", RequestFile: filepath.Join(t.TempDir(), "request.json"), JSONOutput: true, Stdout: &bytes.Buffer{}, FromWorkspace: mode == "incoming", FromWorkspaceNow: mode != "incoming"}
			var broker *childbroker.Server
			if mode != "direct-current" {
				sdk, e := workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, e)
				engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt}
				current := jobcontext.Current{TenantID: "0", GoalID: source.GoalID, JobID: "source", InvocationHash: "invocation"}
				resolver := cellref.Registry(rt, "0")
				ctx = jobref.WithScope(ctx, jobref.Scope{ResolveCell: resolver})
				broker, err = childbroker.Start(ctx, childbroker.Options{Current: current, Submitter: engine, Workspace: &workspacesubmit.Source{Git: gitstate.GlobalGitTaskContext{BaseRepo: celltest.Repo(root), BaseRef: "main", ResolvedBaseHash: base, CellName: "active"}, Worktree: root}})
				require.NoError(t, err)
				defer broker.Close()
				for k, v := range jobcontext.EnvForCurrent(current) {
					t.Setenv(k, v)
				}
				for k, v := range broker.Env() {
					t.Setenv(k, v)
				}
			} else {
				h := remote.NewServer(rt)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					h.ServeHTTP(w, r.WithContext(runtimetest.PrimaryGoalContext(r.Context())))
				}))
				defer server.Close()
				opts.JobDBURI = testJobDBURI(server.URL, "0")
				opts.Cell = "active"
				opts.CellRepo = celltest.Repo(root)
			}
			require.NoError(t, Run(ctx, opts))
			raw, e := os.ReadFile(opts.RequestFile)
			require.NoError(t, e)
			var retained retainedSubmission
			require.NoError(t, json.Unmarshal(raw, &retained))
			require.Equal(t, source.GoalID, retained.Descriptor.Job.GoalID)
			if mode != "incoming" {
				require.NotEmpty(t, retained.Descriptor.Artifacts)
			}
			require.NoError(t, os.Remove(recipePath))
			require.NoError(t, os.WriteFile(filepath.Join(root, "value.txt"), []byte("after capture"), 0600))
			var out bytes.Buffer
			require.NoError(t, Run(ctx, Options{JobDBURI: opts.JobDBURI, ReplayFile: opts.RequestFile, JSONOutput: true, Stdout: &out}))
			var ref submitResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &ref))
			require.Equal(t, source.CellID, ref.CellID)
			require.Equal(t, source.GoalID, ref.GoalID)
			if broker != nil {
				require.Equal(t, source.CellID, broker.StartedJobs().Items[0].CellID)
			}
		})
	}
}
func TestSelectedCLIReplayRejectsOverrides(t *testing.T) {
	base := Options{ReplayFile: "saved", TenantID: "t", SWFURL: "http://test"}
	for _, mutate := range []func(*Options){func(o *Options) { o.GoalID = "g" }, func(o *Options) { o.JobID = "j" }, func(o *Options) { o.PromptSet = true }, func(o *Options) { o.ArtifactSpecs = []string{"new"} }, func(o *Options) { o.FromWorkspaceNow = true }, func(o *Options) { o.Cell = "other" }} {
		opts := base
		mutate(&opts)
		require.ErrorContains(t, opts.Validate(), "override")
	}
	opts := Options{TenantID: "t", SWFURL: "http://test", GoalID: "g"}
	require.ErrorContains(t, opts.Validate(), "--job-id")
}
