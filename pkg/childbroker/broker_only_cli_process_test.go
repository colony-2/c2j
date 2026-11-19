package childbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/c2j/pkg/workspacesubmit"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/colony-2/jobdb/pkg/jobdb/runtimetest"
	workflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

const brokerOnlyTenant = "tenant-broker-only"
const brokerOnlyRecipe = `id: broker_only
version: "1"
input_schema:
 prompt: {type: string}
 value: {type: string}
sequence:
 - id: marker
   child_group: {mode: start, children: []}
outputs: {}
`

// Use a complete environment, not process.RunRequest's ambient environment merge.
func brokerOnlyEnvironment(base []string, additions map[string]string) []string {
	out := []string{}
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "C2J_CURRENT_") || strings.HasPrefix(key, "C2J_CHILD_JOB_") || strings.HasPrefix(key, "C2J_WORKSPACE_") || key == "C2J_JOBDB" || key == "C2J_TENANT_ID" || key == "C2J_OBJECT_OUTBOX" || key == "C2J_TEST_SUBMIT_BINARY" {
			continue
		}
		if _, replaced := additions[key]; !replaced {
			out = append(out, entry)
		}
	}
	for k, v := range additions {
		out = append(out, k+"="+v)
	}
	return out
}

type brokerOnlyFixture struct {
	t                                    *testing.T
	ctx                                  context.Context
	binary, root, home, request          string
	current                              jobcontext.Current
	rt                                   *toy.Runtime
	source                               jobref.Ref
	broker                               *Server
	env                                  map[string]string
	attempts, preparations, publications atomic.Int32
	// Called at the HTTP publication boundary, before the worker accepts a job.
	beforePublish func()
	loseResponse  bool
	responseMode  string
	lastRequest   SubmitRequest
}

func newBrokerOnlyFixture(t *testing.T, binary string) *brokerOnlyFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(celltest.Context(runtimetest.PrimaryGoalContext(context.Background())), time.Minute)
	t.Cleanup(cancel)
	f := &brokerOnlyFixture{t: t, ctx: ctx, binary: binary, root: t.TempDir(), home: t.TempDir(), request: filepath.Join(t.TempDir(), "request.json"), rt: toy.New()}
	f.git("init", "-b", "main")
	f.git("config", "user.email", "fixture@example.test")
	f.git("config", "user.name", "Fixture")
	require.NoError(t, os.MkdirAll(filepath.Join(f.root, ".c2"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".c2/config.yaml"), []byte("self:\n  cell: app\n  repo: "+celltest.Repo(f.root)+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "value.txt"), []byte("incoming\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "child.yaml"), []byte(brokerOnlyRecipe), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "attachment.txt"), []byte("attachment café\n"), 0600))
	f.git("add", ".")
	f.git("commit", "-m", "incoming")
	base := f.git("rev-parse", "HEAD")
	celltest.Create(t, f.rt, brokerOnlyTenant, "app", celltest.Repo(f.root))
	sdk, err := workflow.NewEngineBuilder().WithRuntime(f.rt).BuildEngine()
	require.NoError(t, err)
	engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: f.rt}
	src, err := engine.SubmitJobRef(ctx, jobdb.SubmitJob{TenantId: brokerOnlyTenant, JobID: "source", Cell: "app", CellRepo: celltest.Repo(f.root), JobType: "work", Data: jobdb.NewTaskDataOrPanic(nil)})
	require.NoError(t, err)
	f.source = src
	f.current = jobcontext.Current{TenantID: brokerOnlyTenant, JobID: "source", GoalID: src.GoalID, CellID: src.CellID, CellRepo: src.CellRepo, JobType: starter.RecipeJobType, OpType: "command_execution", OpStep: "submit", OpTaskType: "activity:command_execution", CellName: "app", RepositorySource: celltest.Repo(f.root), GitRef: "main", InvocationPath: "sequence.submit", InvocationSequence: 7, InvocationHash: "invocation"}
	profile, err := gitprofile.NewScope(ctx, gitprofile.Selection{Explicit: true}, jobdb.ConfigurationSnapshot{}, nil, binary)
	require.NoError(t, err)
	t.Cleanup(profile.Close)
	ctx = gitprofile.WithScope(ctx, profile)
	ctx = jobref.WithScope(ctx, jobref.Scope{ResolveCell: cellref.Registry(f.rt, brokerOnlyTenant)})
	f.ctx = ctx
	f.broker, err = Start(ctx, Options{Current: f.current, Submitter: engine, Workspace: &workspacesubmit.Source{Git: gitstate.GlobalGitTaskContext{BaseRepo: celltest.Repo(f.root), BaseRef: "main", ResolvedBaseHash: base, CellName: "app"}, Worktree: f.root}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.broker.Close()) })
	handler := f.broker.server.Handler
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			raw, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &f.lastRequest))
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if strings.HasSuffix(r.URL.Path, "/prepare") {
				f.preparations.Add(1)
			} else {
				f.publications.Add(1)
				if f.beforePublish != nil {
					f.beforePublish()
				}
			}
		}
		f.attempts.Add(1)
		if f.responseMode != "" && (strings.HasSuffix(r.URL.Path, "/workspace") || r.Method == http.MethodPost) {
			switch f.responseMode {
			case "error":
				http.Error(w, "fixture rejected", 503)
			case "incomplete":
				_, _ = w.Write([]byte(`{}`))
			case "wrong-receipt":
				_ = json.NewEncoder(w).Encode(SubmitResponse{Cell: "app", CellID: f.source.CellID, CellRepo: f.source.CellRepo, TenantID: "wrong", JobID: f.lastRequest.Start.JobID, GoalID: f.source.GoalID, Recipe: "broker_only"})
			case "malformed":
				_, _ = w.Write([]byte(`{"broken":`))
			case "denied":
				http.Error(w, "denied", 403)
			case "old-workspace":
				http.NotFound(w, r)
			case "invalid-workspace":
				_, _ = w.Write([]byte(`{"version":2,"git":{}}`))
			}
			return
		}
		if f.loseResponse && strings.HasSuffix(r.URL.Path, "/primary-goal") {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			if recorder.Code == 200 {
				_, _ = w.Write([]byte(`{"lost":`))
				return
			}
			for k, v := range recorder.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	f.env = jobcontext.EnvForCurrent(f.current)
	for k, v := range f.broker.Env() {
		f.env[k] = v
	}
	f.env[jobcontext.ChildJobEndpointEnv] = proxy.URL + submitPath
	f.env["HOME"] = f.home
	f.env["XDG_CONFIG_HOME"] = filepath.Join(f.home, "config")
	f.env["XDG_CACHE_HOME"] = filepath.Join(f.home, "cache")
	return f
}
func (f *brokerOnlyFixture) git(args ...string) string {
	f.t.Helper()
	c := exec.CommandContext(f.ctx, "git", args...)
	c.Dir = f.root
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := c.CombinedOutput()
	require.NoError(f.t, err, string(out))
	return strings.TrimSpace(string(out))
}
func (f *brokerOnlyFixture) run(args []string, changes map[string]string) ([]byte, error) {
	f.t.Helper()
	env := map[string]string{}
	for k, v := range f.env {
		env[k] = v
	}
	for k, v := range changes {
		env[k] = v
	}
	// Poisoned inherited route values must be removed before fixture values are added.
	base := append(os.Environ(), "C2J_JOBDB=embed:///", "C2J_CURRENT_TENANT_ID=poison", "C2J_CHILD_JOB_TOKEN=poison", "C2J_WORKSPACE_ROOT=/poison", "C2J_TENANT_ID=poison", "C2J_OBJECT_OUTBOX=/poison")
	c := exec.CommandContext(f.ctx, f.binary, append([]string{"submit"}, args...)...)
	c.Dir = f.root
	c.Env = brokerOnlyEnvironment(base, env)
	var out, stderr bytes.Buffer
	c.Stdout = &out
	c.Stderr = &stderr
	err := c.Run()
	if err != nil {
		return append(out.Bytes(), stderr.Bytes()...), err
	}
	return out.Bytes(), nil
}
func (f *brokerOnlyFixture) args() []string {
	return []string{"instructions café", "--self", "--advanced-recipe-file", "child.yaml", "--artifact", "notes=attachment.txt", "--inputs-json", `{"value":"exact"}`, "--require-base", "ubuntu-26*", "--require-capability", "docker", "--json"}
}
func (f *brokerOnlyFixture) inventory(n int) {
	f.t.Helper()
	jobs, err := f.rt.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{brokerOnlyTenant}})
	require.NoError(f.t, err)
	require.Len(f.t, jobs.Jobs, n+1, "source plus accepted children")
	unique := map[string]jobcontext.StartedJobContext{}
	for _, item := range f.broker.StartedJobs().Items {
		if prior, ok := unique[item.JobID]; ok {
			require.Equal(f.t, prior, item)
		}
		unique[item.JobID] = item
	}
	require.Len(f.t, unique, n)
	require.NoError(f.t, filepath.Walk(f.home, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, e := filepath.Rel(f.home, path)
		require.NoError(f.t, e)
		require.NotContains(f.t, relative, ".db")
		require.NotContains(f.t, relative, ".lock")
		require.NotContains(f.t, relative, "embedded")
		require.NotContains(f.t, relative, filepath.Join(".c2", "embed"))
		return nil
	}))
}
func (f *brokerOnlyFixture) checkSubmission(raw []byte, goal string) SubmitResponse {
	f.t.Helper()
	var ref SubmitResponse
	require.NoError(f.t, json.Unmarshal(raw, &ref), string(raw))
	require.Equal(f.t, SubmitResponse{Cell: "app", CellID: f.source.CellID, CellRepo: f.source.CellRepo, TenantID: brokerOnlyTenant, JobID: ref.JobID, GoalID: ref.GoalID, Recipe: "broker_only"}, ref)
	require.NotEmpty(f.t, ref.JobID)
	require.NotEmpty(f.t, ref.GoalID)
	if goal != "" {
		require.Equal(f.t, goal, ref.GoalID)
	}
	key := jobdb.JobKey{TenantId: ref.TenantID, JobId: ref.JobID}
	chapters, err := f.rt.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: key})
	require.NoError(f.t, err)
	require.NotEmpty(f.t, chapters)
	body, ok := chapters[0].Body.(jobdb.JobStartChapter)
	require.True(f.t, ok)
	var start workflowctl.StartJob
	require.NoError(f.t, json.Unmarshal(body.Input.Data, &start))
	expectedParent := jobcontext.ParentFromCurrent(f.current)
	require.Equal(f.t, &expectedParent, start.Parent)
	stored, err := f.rt.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{brokerOnlyTenant}, JobKeys: []jobdb.JobKey{key}})
	require.NoError(f.t, err)
	var metadata starter.JobMetadata
	require.NoError(f.t, json.Unmarshal(stored.Jobs[0].Metadata, &metadata))
	require.Equal(f.t, starter.JobMetadataFromStartJob(start), metadata)
	require.Equal(f.t, gitprofile.Selection{Explicit: true}, start.JobContext.GitProfile)
	require.Equal(f.t, map[string]any{"prompt": "instructions café", "value": "exact"}, start.Inputs)
	require.NotNil(f.t, start.Execution.JobRequirements.Base)
	require.Equal(f.t, "ubuntu-26*", *start.Execution.JobRequirements.Base)
	require.Equal(f.t, []string{"docker"}, start.Execution.JobRequirements.Capabilities)
	require.Equal(f.t, start.Execution.JobRequirements, start.Execution.Effective)
	found := map[string][]byte{}
	for _, a := range chapters[0].Artifacts {
		art, e := f.rt.OpenArtifact(f.ctx, jobdb.ArtifactRef{JobKey: key, Ordinal: chapters[0].Ordinal, Name: a.Name, Digest: a.Digest})
		require.NoError(f.t, e)
		r, e := art.Open()
		require.NoError(f.t, e)
		b, e := io.ReadAll(r)
		require.NoError(f.t, e)
		require.NoError(f.t, r.Close())
		found[a.Name] = b
	}
	require.Equal(f.t, []byte("attachment café\n"), found["notes"])
	require.Contains(f.t, string(found["broker_only.recipe.yaml"]), "child_group")
	if len(f.lastRequest.EmbeddedRecipes) > 0 {
		payload := f.lastRequest.EmbeddedRecipes[0]
		require.Equal(f.t, compiledRecipeFormat, payload.Format)
		require.Equal(f.t, "broker_only", payload.Name)
		require.Equal(f.t, payload.YAML, found["broker_only.recipe.yaml"])
	}
	f.inventory(1)
	return ref
}

func TestBrokerOnlyCLI(t *testing.T) {
	binary := os.Getenv("C2J_TEST_SUBMIT_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "c2j")
		c := exec.Command("go", "build", "-mod=readonly", "-o", binary, "./cmd/c2j")
		c.Dir = testRepoRoot(t)
		c.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := c.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	t.Run("optional-direct", func(t *testing.T) {
		f := newBrokerOnlyFixture(t, binary)
		var calls atomic.Int32
		trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Error(w, "direct runtime forbidden", 500)
		}))
		defer trap.Close()
		args := append(f.args(), "--jobdb", trap.URL+"/"+brokerOnlyTenant)
		out, err := f.run(args, nil)
		require.NoError(t, err, string(out))
		f.checkSubmission(out, "")
		require.Zero(t, calls.Load())
	})
	brokerOnlyPositiveCases(t, binary)
	brokerOnlyNegativeCases(t, binary)
	brokerOnlyReplayCases(t, binary)
	t.Run("ordinary", func(t *testing.T) {
		f := newBrokerOnlyFixture(t, binary)
		out, err := f.run(f.args(), nil)
		require.NoError(t, err, string(out))
		f.checkSubmission(out, "")
		require.EqualValues(t, 1, f.publications.Load())
	})
}

// Preparation through the worker gives replay failures an authentic descriptor,
// without publishing a destination child or depending on CLI submission success.
func (f *brokerOnlyFixture) retained() []byte {
	f.t.Helper()
	rec, err := recipe.LoadRecipeFromReader(strings.NewReader(brokerOnlyRecipe))
	require.NoError(f.t, err)
	req, err := NewSubmitRequest(f.ctx, workflowctl.StartJob{TenantId: brokerOnlyTenant, JobID: "dest", GoalID: f.source.GoalID, Cell: "app", CellRepo: f.source.CellRepo, RecipeName: "broker_only", Inputs: map[string]any{"prompt": "instructions café", "value": "exact"}}, []jobdb.Artifact{jobdb.NewArtifactFromBytes("notes", []byte("attachment café\n"))}, *rec)
	require.NoError(f.t, err)
	d, err := PrepareSelected(f.ctx, brokerConfig(f.t, f.broker), req)
	require.NoError(f.t, err)
	raw, err := json.Marshal(struct {
		Version    int                `json:"version"`
		RuntimeURL string             `json:"runtime_url"`
		TenantID   string             `json:"tenant_id"`
		Broker     bool               `json:"broker"`
		Descriptor starter.Descriptor `json:"descriptor"`
	}{1, "", brokerOnlyTenant, true, d})
	require.NoError(f.t, err)
	require.NoError(f.t, os.WriteFile(f.request, raw, 0600))
	return raw
}
func (f *brokerOnlyFixture) selectedArgs() []string {
	return append(f.args(), "--goal", f.source.GoalID, "--job-id", "dest", "--request-file", f.request)
}
func (f *brokerOnlyFixture) assertUnchanged(raw []byte) {
	f.t.Helper()
	after, err := os.ReadFile(f.request)
	require.NoError(f.t, err)
	require.Equal(f.t, raw, after)
}
func brokerOnlyPositiveCases(t *testing.T, binary string) {
	for _, mode := range []string{"incoming", "current", "selected", "lost-response"} {
		t.Run(mode, func(t *testing.T) {
			f := newBrokerOnlyFixture(t, binary)
			args := f.args()
			if mode == "incoming" || mode == "current" {
				require.NoError(t, os.WriteFile(filepath.Join(f.root, "committed.txt"), []byte("new commit\n"), 0600))
				f.git("add", "committed.txt")
				f.git("commit", "-m", "during task")
				require.NoError(t, os.WriteFile(filepath.Join(f.root, "value.txt"), []byte("current\n"), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(f.root, "untracked.txt"), []byte("untracked\n"), 0600))
				flag := "--from-workspace"
				if mode == "current" {
					flag = "--from-workspace-now"
				}
				args = append(args, flag)
			} else {
				args = append(f.selectedArgs(), "--from-workspace-now")
				f.loseResponse = mode == "lost-response"
				f.beforePublish = func() {
					raw, err := os.ReadFile(f.request)
					require.NoError(t, err)
					var retained struct {
						RuntimeURL string             `json:"runtime_url"`
						TenantID   string             `json:"tenant_id"`
						Broker     bool               `json:"broker"`
						Descriptor starter.Descriptor `json:"descriptor"`
					}
					require.NoError(t, json.Unmarshal(raw, &retained))
					require.Empty(t, retained.RuntimeURL)
					require.True(t, retained.Broker)
					require.Equal(t, brokerOnlyTenant, retained.TenantID)
					saved, err := json.Marshal(retained.Descriptor)
					require.NoError(t, err)
					sent, err := json.Marshal(f.lastRequest.Descriptor)
					require.NoError(t, err)
					require.JSONEq(t, string(saved), string(sent))
					info, err := os.Stat(f.request)
					require.NoError(t, err)
					require.Equal(t, os.FileMode(0600), info.Mode().Perm())
				}
			}
			beforeHead := f.git("rev-parse", "HEAD")
			beforeStatus := f.git("status", "--porcelain=v1")
			out, err := f.run(args, nil)
			if mode == "lost-response" {
				require.Error(t, err)
				f.inventory(1)
			} else {
				require.NoError(t, err, string(out))
				goal := ""
				if mode == "selected" {
					goal = f.source.GoalID
				}
				f.checkSubmission(out, goal)
			}
			require.Equal(t, beforeHead, f.git("rev-parse", "HEAD"))
			require.Equal(t, beforeStatus, f.git("status", "--porcelain=v1"))
			if mode == "incoming" || mode == "current" {
				var ref SubmitResponse
				require.NoError(t, json.Unmarshal(out, &ref))
				start := f.start(ref)
				j := start.JobContext
				c := j.InitialCommit
				require.NotNil(t, c)
				restored := filepath.Join(t.TempDir(), "restored")
				task := &gitstate.GitTaskContext{WorktreePath: restored, GlobalGitTaskContext: &gitstate.GlobalGitTaskContext{BaseRepo: j.GitBase.BaseRepo, BaseRef: j.GitBase.BaseRef, ResolvedBaseHash: j.GitBase.ResolvedBaseHash, Workspace: j.Workspace, PersistHash: c.PersistHash, ParentHash: c.ParentHash, ParentRef: c.ParentRef}}
				a := f.artifact(ref, workspacesubmit.ArtifactName)
				require.NoError(t, gitstate.NewController(nil).Restore(f.ctx, task, a))
				value, err := os.ReadFile(filepath.Join(restored, "value.txt"))
				require.NoError(t, err)
				if mode == "incoming" {
					require.Equal(t, "incoming\n", string(value))
					_, err = os.Stat(filepath.Join(restored, "committed.txt"))
					require.True(t, os.IsNotExist(err))
					_, err = os.Stat(filepath.Join(restored, "untracked.txt"))
					require.True(t, os.IsNotExist(err))
				} else {
					require.Equal(t, "current\n", string(value))
					value, err = os.ReadFile(filepath.Join(restored, "committed.txt"))
					require.NoError(t, err)
					require.Equal(t, "new commit\n", string(value))
					value, err = os.ReadFile(filepath.Join(restored, "untracked.txt"))
					require.NoError(t, err)
					require.Equal(t, "untracked\n", string(value))
				}
			} else {
				raw, err := os.ReadFile(f.request)
				require.NoError(t, err)
				require.NoError(t, os.Remove(filepath.Join(f.root, "child.yaml")))
				require.NoError(t, os.Remove(filepath.Join(f.root, "attachment.txt")))
				require.NoError(t, os.WriteFile(filepath.Join(f.root, "value.txt"), []byte("after capture"), 0600))
				f.loseResponse = false
				out, err = f.run([]string{"--replay", f.request, "--json"}, nil)
				require.NoError(t, err, string(out))
				f.checkSubmission(out, f.source.GoalID)
				f.assertUnchanged(raw)
				require.EqualValues(t, 1, f.preparations.Load())
				require.EqualValues(t, 2, f.publications.Load())
			}
		})
	}
}
func (f *brokerOnlyFixture) start(ref SubmitResponse) workflowctl.StartJob {
	f.t.Helper()
	chapters, err := f.rt.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: jobdb.JobKey{TenantId: ref.TenantID, JobId: ref.JobID}})
	require.NoError(f.t, err)
	var start workflowctl.StartJob
	require.NoError(f.t, json.Unmarshal(chapters[0].Body.(jobdb.JobStartChapter).Input.Data, &start))
	return start
}
func (f *brokerOnlyFixture) artifact(ref SubmitResponse, name string) jobdb.Artifact {
	f.t.Helper()
	key := jobdb.JobKey{TenantId: ref.TenantID, JobId: ref.JobID}
	chapters, err := f.rt.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: key})
	require.NoError(f.t, err)
	for _, a := range chapters[0].Artifacts {
		if a.Name == name {
			art, err := f.rt.OpenArtifact(f.ctx, jobdb.ArtifactRef{JobKey: key, Ordinal: chapters[0].Ordinal, Name: a.Name, Digest: a.Digest})
			require.NoError(f.t, err)
			r, e := art.Open()
			require.NoError(f.t, e)
			data, e := io.ReadAll(r)
			require.NoError(f.t, e)
			require.NoError(f.t, r.Close())
			return jobdb.NewArtifactFromBytes(name, data)
		}
	}
	return nil
}

func brokerOnlyNegativeCases(t *testing.T, binary string) {
	for _, path := range []string{"ordinary", "selected", "replay"} {
		t.Run("reject-"+path, func(t *testing.T) {
			for _, fault := range []string{"missing-current", "partial-current", "bad-sequence", "bad-version", "partial-broker", "missing-broker", "invalid-endpoint", "unavailable", "bad-token", "bad-session", "denied", "error", "malformed", "incomplete", "wrong-receipt", "invalid-direct", "invalid-project", "tenant-mismatch", "embedded-mismatch", "run"} {
				t.Run(fault, func(t *testing.T) {
					f := newBrokerOnlyFixture(t, binary)
					args := f.args()
					var retained []byte
					if path == "selected" {
						args = f.selectedArgs()
					}
					if path == "replay" {
						retained = f.retained()
						args = []string{"--replay", f.request, "--json"}
					}
					changes := map[string]string{}
					var directCalls atomic.Int32
					trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						directCalls.Add(1)
						http.Error(w, "direct fallback", 500)
					}))
					defer trap.Close()
					// Network failures also have a valid direct route available, so fallback is observable.
					switch fault {
					case "missing-current":
						for k := range f.env {
							if strings.HasPrefix(k, "C2J_CURRENT_") {
								changes[k] = ""
							}
						}
					case "partial-current":
						changes[jobcontext.CurrentGoalIDEnv] = ""
					case "bad-sequence":
						changes[jobcontext.CurrentInvocationSequenceEnv] = "seven"
					case "bad-version":
						changes[jobcontext.CurrentContextVersionEnv] = "2"
					case "missing-broker":
						for k := range f.env {
							if strings.HasPrefix(k, "C2J_CHILD_JOB_") {
								changes[k] = ""
							}
						}
					case "partial-broker":
						changes[jobcontext.ChildJobSessionIDEnv] = ""
					case "invalid-endpoint":
						changes[jobcontext.ChildJobEndpointEnv] = "http://secret@worker.test/path?query=1"
					case "unavailable":
						closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
						endpoint := closed.URL
						closed.Close()
						changes[jobcontext.ChildJobEndpointEnv] = endpoint + submitPath
					case "bad-token":
						changes[jobcontext.ChildJobTokenEnv] = "wrong"
					case "bad-session":
						changes[jobcontext.ChildJobSessionIDEnv] = "wrong"
					case "denied", "error", "malformed", "incomplete", "wrong-receipt":
						f.responseMode = fault
					case "invalid-project":
						require.NoError(t, os.WriteFile(filepath.Join(f.root, ".c2/config.yaml"), []byte("[invalid"), 0600))
					case "invalid-direct":
						args = append(args, "--jobdb", "ftp://invalid/tenant")
					case "tenant-mismatch":
						args = append(args, "--jobdb", trap.URL+"/other")
					case "embedded-mismatch":
						args = append(args, "--embed")
					case "run":
						for i, a := range args {
							if a == "--json" {
								args = append(args[:i], args[i+1:]...)
								break
							}
						}
						args = append(args, "--run")
					}
					if fault == "unavailable" || fault == "bad-token" || fault == "bad-session" || fault == "denied" || fault == "error" || fault == "malformed" || fault == "incomplete" || fault == "wrong-receipt" {
						args = append(args, "--jobdb", trap.URL+"/"+brokerOnlyTenant)
						if path == "replay" {
							var doc map[string]any
							require.NoError(t, json.Unmarshal(retained, &doc))
							doc["runtime_url"] = trap.URL
							retained, _ = json.Marshal(doc)
							require.NoError(t, os.WriteFile(f.request, retained, 0600))
						}
					}
					out, err := f.run(args, changes)
					require.Error(t, err, string(out))
					require.Zero(t, directCalls.Load())
					f.inventory(0)
					if path == "replay" {
						f.assertUnchanged(retained)
					}
					if fault == "run" {
						require.Contains(t, string(out), "--run requires direct")
						require.Zero(t, f.attempts.Load())
						require.Zero(t, f.preparations.Load())
						require.Zero(t, f.publications.Load())
						if path == "selected" {
							_, err = os.Stat(f.request)
							require.True(t, os.IsNotExist(err))
						}
					}
				})
			}
		})
	}
	for _, fault := range []string{"old-workspace", "invalid-workspace", "missing-workspace", "unknown-target", "repo-mismatch", "workspace-target", "retention-exists", "retention-directory"} {
		t.Run(fault, func(t *testing.T) {
			f := newBrokerOnlyFixture(t, binary)
			args := f.args()
			switch fault {
			case "old-workspace", "invalid-workspace":
				f.responseMode = fault
				args = append(args, "--from-workspace-now")
			case "missing-workspace":
				f.broker.workspace = nil
				args = append(args, "--from-workspace-now")
			case "unknown-target", "workspace-target":
				for i, a := range args {
					if a == "--self" {
						args = append(args[:i], args[i+1:]...)
						break
					}
				}
				args = append(args, "--cell", "unknown")
				if fault == "workspace-target" {
					celltest.Create(t, f.rt, brokerOnlyTenant, "unknown", "example.test/other")
					args = append(args, "--from-workspace-now")
				}
			case "repo-mismatch":
				args = append(args, "--cell-repo", "example.test/wrong")
			case "retention-exists":
				args = f.selectedArgs()
				require.NoError(t, os.WriteFile(f.request, []byte("existing"), 0600))
			case "retention-directory":
				args = f.selectedArgs()
				require.NoError(t, os.Mkdir(f.request, 0700))
			}
			out, err := f.run(args, nil)
			require.Error(t, err, string(out))
			if fault != "workspace-target" {
				require.Zero(t, f.publications.Load())
			}
			f.inventory(0)
			if fault == "retention-exists" {
				f.assertUnchanged([]byte("existing"))
			}
		})
	}
}

func brokerOnlyReplayCases(t *testing.T, binary string) {
	for _, fault := range []string{"route-to-direct", "route-from-direct", "added-destination", "removed-destination", "changed-destination", "tenant", "envelope-version", "descriptor-version", "artifact-integrity", "parent", "invocation", "profile", "renewed-session", "override", "existing-mismatch"} {
		t.Run("replay-"+fault, func(t *testing.T) {
			f := newBrokerOnlyFixture(t, binary)
			raw := f.retained()
			args := []string{"--replay", f.request, "--json"}
			changes := map[string]string{}
			var doc struct {
				Version    int                `json:"version"`
				RuntimeURL string             `json:"runtime_url"`
				TenantID   string             `json:"tenant_id"`
				Broker     bool               `json:"broker"`
				Descriptor starter.Descriptor `json:"descriptor"`
			}
			require.NoError(t, json.Unmarshal(raw, &doc))
			switch fault {
			case "route-from-direct":
				doc.Broker = false
			case "route-to-direct":
				for k := range f.env {
					if strings.HasPrefix(k, "C2J_CHILD_JOB_") {
						changes[k] = ""
					}
				}
				doc.RuntimeURL = "http://127.0.0.1:1"
				args = append(args, "--jobdb", doc.RuntimeURL+"/"+brokerOnlyTenant)
			case "added-destination":
				args = append(args, "--jobdb", "http://127.0.0.1:1/"+brokerOnlyTenant)
			case "removed-destination":
				doc.RuntimeURL = "http://127.0.0.1:1"
			case "changed-destination":
				doc.RuntimeURL = "http://127.0.0.1:1"
				args = append(args, "--jobdb", "http://127.0.0.1:2/"+brokerOnlyTenant)
			case "tenant":
				changes[jobcontext.CurrentTenantIDEnv] = "different"
			case "envelope-version":
				doc.Version = 2
			case "descriptor-version":
				doc.Descriptor.Version = 2
			case "artifact-integrity":
				require.NotEmpty(t, doc.Descriptor.Artifacts)
				doc.Descriptor.Artifacts[0].Data = []byte("corrupt")
			case "parent", "invocation", "profile", "renewed-session":
				current := f.current
				ctx := f.ctx
				if fault == "parent" {
					current.JobID = "another-parent"
				}
				if fault == "invocation" {
					current.InvocationSequence++
					current.InvocationHash = "another-invocation"
				}
				if fault == "profile" {
					profile, err := gitprofile.NewScope(ctx, gitprofile.Selection{}, jobdb.ConfigurationSnapshot{}, nil, binary)
					require.NoError(t, err)
					t.Cleanup(profile.Close)
					ctx = gitprofile.WithScope(ctx, profile)
				}
				fresh, err := Start(ctx, Options{Current: current, Submitter: f.broker.submitter, Workspace: f.broker.workspace})
				require.NoError(t, err)
				require.NoError(t, f.broker.Close())
				f.broker = fresh
				for k, v := range fresh.Env() {
					changes[k] = v
				}
				changes[jobcontext.ChildJobEndpointEnv] = fresh.localEndpoint
				for k, v := range jobcontext.EnvForCurrent(current) {
					changes[k] = v
				}
			case "override":
				args = append(args, "--inputs-json", `{"value":"override"}`)
			case "existing-mismatch":
				out, err := f.run(args, nil)
				require.NoError(t, err, string(out))
				f.inventory(1)
				var metadata map[string]any
				require.NoError(t, json.Unmarshal(doc.Descriptor.Job.Metadata, &metadata))
				metadata["title"] = "changed"
				doc.Descriptor.Job.Metadata, _ = json.Marshal(metadata)
			}
			if fault != "renewed-session" {
				raw, _ = json.Marshal(doc)
				require.NoError(t, os.WriteFile(f.request, raw, 0600))
			}
			out, err := f.run(args, changes)
			if fault == "renewed-session" {
				require.NoError(t, err, string(out))
				var ref SubmitResponse
				require.NoError(t, json.Unmarshal(out, &ref))
				require.Equal(t, SubmitResponse{Cell: "app", CellID: f.source.CellID, CellRepo: f.source.CellRepo, TenantID: brokerOnlyTenant, JobID: "dest", GoalID: f.source.GoalID, Recipe: "broker_only"}, ref)
				f.inventory(1)
			} else {
				require.Error(t, err, string(out))
				if fault == "existing-mismatch" {
					require.Contains(t, string(out), "does not match request")
					f.inventory(1)
				} else {
					f.inventory(0)
				}
			}
			f.assertUnchanged(raw)
		})
	}
}
