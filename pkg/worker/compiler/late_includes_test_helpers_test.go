package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/repository"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/git/selectorcache"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type lateGitFixture struct {
	dir, url, initial string
	reads             atomic.Int32
	available         atomic.Bool
	beforeRead        func()
}

func newLateGitFixture(t *testing.T) *lateGitFixture {
	t.Helper()
	root := t.TempDir()
	f := &lateGitFixture{dir: filepath.Join(root, "team", "repo")}
	f.initial = workspaceRepo(t, f.dir, "A")
	f.available.Store(true)
	git, err := exec.LookPath("git")
	require.NoError(t, err)
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Dir: root, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.reads.Add(1)
		if f.beforeRead != nil {
			f.beforeRead()
		}
		if !f.available.Load() {
			http.Error(w, "source removed", http.StatusServiceUnavailable)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	f.url = fmt.Sprintf("https://late.test/fixture-%s.git", sha256Hex([]byte(f.dir))[:16])
	identity := permissions.RepositoryIdentity(f.url)
	lateTransports.Store(identity, server.URL+"/team/repo")
	t.Cleanup(func() { lateTransports.Delete(identity) })
	return f
}
func (f *lateGitFixture) selector(p, commit string) string {
	return fmt.Sprintf("git+%s//%s@%s", f.url, p, commit)
}
func (f *lateGitFixture) expand(t *testing.T, body string) recipe.Recipe {
	t.Helper()
	rec, err := recipe.LoadRecipeFromString([]byte(body))
	require.NoError(t, err)
	source := RecipeSourceResolution{SourceKind: RecipeSourceKindGit, ResolvedSelector: f.selector("root.yaml", f.initial), ResolvedCommit: f.initial}
	expanded, err := ResolveInlineRecipes(lateTestContext(), *rec, InlineResolutionOptions{RootSource: &source})
	require.NoError(t, err)
	return expanded.Recipe
}
func lateWorker(t *testing.T, rec recipe.Recipe, inputs func() map[string]any) jobworkflow.ExecutionWorker {
	t.Helper()
	job, git := GenerateTestContext()
	return jobworkflow.ExecutionWorker{JobType: "late-include-test", Execute: func(exec jobworkflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		raw := map[string]any{}
		if inputs != nil {
			raw = inputs()
		}
		out, arts, err := ExecuteRecipe(workflow.Context{JobContext: exec, ServiceDependencies2: catchWorkflowContext(exec).ServiceDependencies2}, rec, raw, job, git)
		if err != nil {
			return nil, err
		}
		return jobdb.NewTaskData(out, arts...)
	}}
}
func lateTasks(t *testing.T, guard *executionruntime.Runtime, extra ...jobworkflow.TaskWorker) []jobworkflow.TaskWorker {
	t.Helper()
	resolver := NewRecipeSourceResolver(RecipeSourceResolverOptions{SelectorCache: &selectorcache.Cache{Root: t.TempDir()}})
	workers := []jobworkflow.TaskWorker{NewCellContextTaskWorker(), lateSelectWorker{}, lateCaptureWorker{resolver: resolver}}
	workers = append(workers, extra...)
	for i := range workers {
		workers[i] = guard.WrapTaskWorker(workers[i])
	}
	return workers
}
func lateRun(t *testing.T, rt jobdb.ConsumerRuntime, key jobdb.JobKey, w jobworkflow.ExecutionWorker, tasks ...jobworkflow.TaskWorker) jobworkflow.JobRunOutcome {
	t.Helper()
	h, err := jobworkflow.GetJobForRun(lateTestContext(), rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: w, TaskWorkers: tasks, WorkerID: "late-tests", LeaseDuration: time.Minute})
	require.NoError(t, err)
	out, err := h.Run(nil)
	require.NoError(t, err)
	return out
}
func lateGuard(rt jobdb.ConsumerRuntime) *executionruntime.Runtime {
	guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
	guard.Issuer = permissions.JobDBIssuer{}
	return guard
}
func lateRecordOutputs(t *testing.T, rt jobdb.ConsumerRuntime, key jobdb.JobKey) ([]lateSelection, []lateCapture, jobdb.GetJobRunResponse) {
	t.Helper()
	run, err := jobdb.GetJobRun(celltest.Context(context.Background()), rt, jobdb.GetJobRunRequest{JobKey: key})
	require.NoError(t, err)
	var selections []lateSelection
	var captures []lateCapture
	for _, e := range run.Execution {
		if e.Record.Kind != "result" || e.Record.Output == nil {
			continue
		}
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(e.Record.Output.Data, &fields))
		if _, ok := fields["invocation"]; ok {
			var s lateSelection
			require.NoError(t, json.Unmarshal(e.Record.Output.Data, &s))
			selections = append(selections, s)
		}
		if _, ok := fields["recipe_yaml_artifact"]; ok {
			var c lateCapture
			require.NoError(t, json.Unmarshal(e.Record.Output.Data, &c))
			captures = append(captures, c)
		}
	}
	return selections, captures, run
}
func lateArtifactBytes(t *testing.T, rt interface {
	OpenArtifact(context.Context, jobdb.ArtifactRef) (jobdb.ArtifactReader, error)
}, key jobdb.JobKey, run jobdb.GetJobRunResponse) [][]byte {
	t.Helper()
	var bytes [][]byte
	for _, e := range run.Execution {
		if e.Record.Output == nil {
			continue
		}
		for _, a := range e.Record.Output.Artifacts {
			if a.Name != ResolvedRecipeYAMLArtifactName {
				continue
			}
			reader, err := rt.OpenArtifact(testBackground(), jobdb.ArtifactRef{JobKey: jobdb.JobKey{TenantId: key.TenantId, JobId: a.Key.JobId}, Ordinal: a.Key.TaskOrdinal, Name: a.Key.Name})
			require.NoError(t, err)
			stream, err := reader.Open()
			require.NoError(t, err)
			b, err := io.ReadAll(stream)
			require.NoError(t, err)
			require.NoError(t, stream.Close())
			bytes = append(bytes, b)
		}
	}
	return bytes
}

var lateTransports sync.Map

func lateTestContext() context.Context {
	base := testBackground()
	return repository.WithRepositoryTransport(base, func(id string) string {
		if value, ok := lateTransports.Load(id); ok {
			return value.(string)
		}
		return repository.MappedTransport(base, id)
	})
}
