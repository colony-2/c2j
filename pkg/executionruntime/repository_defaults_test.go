package executionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// The runner may return on yield while task cleanup still logs.
type repositoryLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *repositoryLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *repositoryLogBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buffer.Bytes())
}

type repositoryTask struct {
	*nodeTask
	defaults execution.Requirements
	reads    int
}

func (w *repositoryTask) RunTask(local context.Context, tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	w.reads++
	if err := execution.AdmitRepository(local, w.defaults, execution.Requirements{}); err != nil {
		return nil, err
	}
	return w.nodeTask.Run(tc, in)
}
func TestRepositoryDefaultsAdmissionResumeAndReplay(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f, tasks := newNodeFixture(t, backend, `id: repository-defaults
execution_needs: {resources: {memory: 1Gi}}
sequence:
 - op: needs_a
 - op: needs_b
`)
			a := &repositoryTask{nodeTask: tasks[0], defaults: execution.Requirements{Base: str("linux*"), Capabilities: []string{"docker"}, Resources: execution.Resources{CPU: str("2"), Memory: str("8Gi"), EphemeralStorage: str("10Gi")}}}
			b := &repositoryTask{nodeTask: tasks[1]}
			workers := []jobworkflow.TaskWorker{a, b}
			alloc := execution.Allocation{SchemaVersion: execution.SchemaVersion, Resources: execution.Resources{Memory: str("1Gi")}}
			out, event := runNode(t, f, alloc, workers)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.NotNil(t, event)
			require.Zero(t, a.runs.Load())
			require.Zero(t, b.runs.Load())
			require.Equal(t, "1Gi", *event.Demand.Effective.Resources.Memory)
			require.Len(t, event.Mismatches, 4)
			require.True(t, event.Demand.Overrides.Empty())
			// Repeating the same mismatch must not promote defaults or republish demand.
			_, again := runNode(t, f, alloc, workers)
			require.NotNil(t, again)
			require.False(t, again.Published)
			alloc = execution.Allocation{SchemaVersion: execution.SchemaVersion, Base: "linux-amd64", Capabilities: []string{"docker"}, Resources: execution.Resources{CPU: str("2"), Memory: str("1Gi"), EphemeralStorage: str("10Gi")}}
			out, event = runNode(t, f, alloc, workers)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.Nil(t, event)
			require.EqualValues(t, 1, a.runs.Load())
			require.EqualValues(t, 1, b.runs.Load())
			// Completed work must not ask for its old repository environment or reread it.
			reads := a.reads
			out, event = runNode(t, f, execution.Allocation{SchemaVersion: execution.SchemaVersion}, workers)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.Nil(t, event)
			require.Equal(t, reads, a.reads)
		})
	}
}

func TestRepositoryDefaultsCapabilityPresence(t *testing.T) {
	for _, decl := range []string{"", "execution_needs: {capabilities: []}\n", "execution_needs: {capabilities: [recipe]}\n"} {
		t.Run(decl, func(t *testing.T) {
			f, tasks := newNodeFixture(t, "toy", "id: defaults-presence\n"+decl+"op: needs_a\n")
			a := &repositoryTask{nodeTask: tasks[0], defaults: execution.Requirements{Capabilities: []string{"docker"}}}
			alloc := execution.Allocation{SchemaVersion: execution.SchemaVersion, Capabilities: []string{"recipe"}}
			out, event := runNode(t, f, alloc, []jobworkflow.TaskWorker{a})
			if decl == "" {
				require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
				require.Equal(t, []string{"docker"}, event.Demand.Effective.Capabilities)
			} else {
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
				require.Nil(t, event)
			}
		})
	}
}

func (w *repositoryTask) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}

func TestRepositoryDefaultsRealRestoredWorkspace(t *testing.T) {
	for _, mode := range []string{"moving", "unchanged", "pinned", "base", "capabilities", "cpu", "memory", "ephemeral-storage"} {
		t.Run(mode, func(t *testing.T) { testRepositoryDefaultsRestoredWorkspace(t, mode) })
	}
}
func testRepositoryDefaultsRestoredWorkspace(t *testing.T, mode string) {
	ctx := celltest.Context(context.Background())
	bin, cache, installRoot := t.TempDir(), t.TempDir(), t.TempDir()
	installLog := filepath.Join(installRoot, "installed")
	t.Setenv("C2J_TOOL_CACHE_DIR", cache)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("C2J_DEFAULTS_INSTALL_LOG", installLog)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "uv"), []byte(`#!/bin/sh
set -eu
echo install >> "$C2J_DEFAULTS_INSTALL_LOG"
mkdir -p "$UV_TOOL_BIN_DIR"
printf '#!/bin/sh
exit 0
' > "$UV_TOOL_BIN_DIR/defaults-tool"
chmod +x "$UV_TOOL_BIN_DIR/defaults-tool"
`), 0700))
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".c2"), 0700))
	write := func(s string) {
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".c2/config.yaml"), []byte(s), 0600))
		git("add", ".")
		git("commit", "-m", "config")
	}
	write("packages: [uv:defaults-tool==1]\nexecution: {base: linux*, capabilities: [docker], resources: {cpu: 2, memory: 8Gi, ephemeral-storage: 10Gi}}")
	refName := "main"
	if mode == "pinned" {
		cmd := exec.Command("git", "-C", repo, "rev-parse", "HEAD")
		raw, err := cmd.Output()
		require.NoError(t, err)
		refName = strings.TrimSpace(string(raw))
	}
	base := toy.New()
	celltest.Create(t, base, "tenant", "local", celltest.Repo(repo))
	calls := 0
	op := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "repository_defaults_real"}, func(deps coreops.OpDependencies, ctx context.Context, in struct{}) (map[string]any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	coreops.Register(op)
	reg, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(base).BuildEngine()
	require.NoError(t, err)
	r, err := recipe.LoadRecipeFromString([]byte("id: repository-real\nop: repository_defaults_real\n"))
	require.NoError(t, err)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "local"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: refName}}
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", RecipeName: "repository-real", JobContext: job, GitRef: refName, Cell: "local", CellRepo: celltest.Repo(repo)}, jobdbschema.WorkflowEngine{Engine: engine, Registry: base}, *r)
	require.NoError(t, err)
	var logs repositoryLogBuffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	run := func(alloc execution.Allocation) (jobworkflow.JobRunOutcome, *compiler.ExecutionHandoff) {
		rt := New(base, alloc)
		ws, err := compiler.NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().Build(), reg, compiler.RecipeJobWorkerOptions{Allocation: alloc, StageExecution: rt.Stage, WrapTaskWorker: rt.WrapTaskWorker})
		require.NoError(t, err)
		taskWorkers := make([]jobworkflow.TaskWorker, 0, len(ws.TaskWorkers))
		for _, tw := range ws.TaskWorkers {
			taskWorkers = append(taskWorkers, tw)
		}
		runnable, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: ref.Key(), JobWorker: ws.JobWorker, TaskWorkers: taskWorkers, WorkerID: "repository-real", LeaseDuration: time.Minute, Logger: logger})
		require.NoError(t, err)
		out, err := runnable.Run(nil)
		require.NoError(t, err)
		event, err := compiler.ExecutionHandoffFromOutcome(out)
		require.NoError(t, err)
		return out, event
	}
	matching := execution.Allocation{SchemaVersion: execution.SchemaVersion, Base: "linux-amd64", Capabilities: []string{"docker"}, Resources: execution.Resources{CPU: str("2"), Memory: str("8Gi"), EphemeralStorage: str("10Gi")}}
	initial := matching
	switch mode {
	case "base":
		initial.Base = ""
	case "capabilities":
		initial.Capabilities = nil
	case "cpu":
		initial.Resources.CPU = nil
	case "memory":
		initial.Resources.Memory = nil
	case "ephemeral-storage":
		initial.Resources.EphemeralStorage = nil
	default:
		initial = execution.Allocation{SchemaVersion: execution.SchemaVersion}
	}
	out, event := run(initial)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status, "%+v", out)
	require.NotNil(t, event)
	require.Zero(t, calls)
	require.Equal(t, "8Gi", *event.Demand.Effective.Resources.Memory)
	require.Equal(t, "2", *event.Demand.Effective.Resources.CPU)
	require.Equal(t, "10Gi", *event.Demand.Effective.Resources.EphemeralStorage)
	_, statErr := os.Stat(installLog)
	require.True(t, os.IsNotExist(statErr), "installer ran before handoff")
	foundProvenance := false
	for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
		var record map[string]any
		if json.Unmarshal(line, &record) == nil && record["msg"] == "Repository config restored" {
			foundProvenance = true
			require.Equal(t, ".c2/config.yaml", record["config_path"])
			require.Contains(t, record["config_digest"], "sha256:")
			require.NotEmpty(t, record["workspace_scope"])
			require.NotEmpty(t, record["repository"])
			require.NotEmpty(t, record["restored_revision"])
		}
	}
	require.True(t, foundProvenance, "handoff retains config provenance in task logs")
	if mode != "moving" && mode != "unchanged" && mode != "pinned" {
		require.Len(t, event.Mismatches, 1)
		require.Contains(t, event.Mismatches[0].Field, mode)
	}
	// A floating ref is restored again; ordinary commit refs retain their revision.
	if mode == "moving" || mode == "pinned" {
		write("packages: [uv:defaults-tool==1]\nexecution: {base: other, resources: {memory: 64Gi}}")
	}
	out, event = run(matching)
	if mode == "moving" {
		require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
		require.NotNil(t, event)
		require.Equal(t, "other", *event.Demand.Effective.Base)
		require.Equal(t, "64Gi", *event.Demand.Effective.Resources.Memory)
		require.True(t, event.Demand.Overrides.Empty())
		require.Zero(t, calls)
		_, statErr = os.Stat(installLog)
		require.True(t, os.IsNotExist(statErr))
		out, event = run(execution.Allocation{SchemaVersion: execution.SchemaVersion, Base: "other", Resources: execution.Resources{Memory: str("64Gi")}})
	}
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%+v", out)
	require.Nil(t, event)
	require.Equal(t, 1, calls)
	installs, err := os.ReadFile(installLog)
	require.NoError(t, err)
	require.Equal(t, "install\n", string(installs))
	require.NoError(t, os.RemoveAll(repo))
	require.NoError(t, os.RemoveAll(bin))
	require.NoError(t, os.RemoveAll(cache))
	out, event = run(execution.Allocation{SchemaVersion: execution.SchemaVersion})
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.Equal(t, 1, calls)
}

// Exercise actual compiler scope staging and task-input reconstruction, then
// assert published demand after the live repository admission boundary.
func TestRepositoryDefaultsScalarScopes(t *testing.T) {
	defaults := execution.Requirements{Base: str("repo"), Capabilities: []string{"repo"}, Resources: execution.Resources{CPU: str("8"), Memory: str("8Gi"), EphemeralStorage: str("8Gi")}}
	for _, field := range []string{"base", "cpu", "memory", "ephemeral-storage"} {
		for _, scope := range []string{"recipe", "node"} {
			t.Run(field+"/"+scope, func(t *testing.T) {
				decl := "{base: closer}"
				expected := execution.Overlay(defaults, execution.Requirements{})
				switch field {
				case "base":
					expected.Base = str("closer")
				case "cpu":
					decl = "{resources: {cpu: 1}}"
					expected.Resources.CPU = str("1")
				case "memory":
					decl = "{resources: {memory: 1Gi}}"
					expected.Resources.Memory = str("1Gi")
				case "ephemeral-storage":
					decl = "{resources: {ephemeral-storage: 1Gi}}"
					expected.Resources.EphemeralStorage = str("1Gi")
				}
				source := "id: defaults-scalars\nexecution_needs: " + decl + "\nsequence:\n - op: needs_a\n"
				if scope == "node" {
					source = "id: defaults-scalars\nsequence:\n - op: needs_a\n   execution_needs: " + decl + "\n"
				}
				f, tasks := newNodeFixture(t, "toy", source)
				a := &repositoryTask{nodeTask: tasks[0], defaults: defaults}
				out, event := runNode(t, f, execution.Allocation{SchemaVersion: execution.SchemaVersion, Base: "closer", Resources: execution.Resources{CPU: str("1"), Memory: str("1Gi"), EphemeralStorage: str("1Gi")}}, []jobworkflow.TaskWorker{a})
				require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
				require.NotNil(t, event)
				require.Equal(t, expected, event.Demand.Effective)
				require.Zero(t, a.runs.Load())
			})
		}
	}
}

func TestRepositoryDefaultsStrongerSubmissionAndDirective(t *testing.T) {
	for _, layer := range []string{"submission", "directive"} {
		t.Run(layer, func(t *testing.T) {
			f, tasks := newNodeFixture(t, "toy", "id: stronger-defaults\nsequence:\n - op: needs_a\n - op: needs_b\n")
			strong := execution.Requirements{Base: str("strong"), Capabilities: []string{"strong"}, Resources: execution.Resources{CPU: str("1"), Memory: str("1Gi"), EphemeralStorage: str("1Gi")}}
			if layer == "submission" {
				r, err := recipe.LoadRecipeFromString([]byte("id: stronger-submission\nsequence:\n - op: needs_a\n - op: needs_b\n"))
				require.NoError(t, err)
				submitNodeRecipe(t, f, *r, starter.StartRecipeJobOptions{ExecutionRequirements: strong})
			} else {
				tasks[0].patch = &strong
			}
			defaults := execution.Requirements{Base: str("repo"), Capabilities: []string{"repo"}, Resources: execution.Resources{CPU: str("8"), Memory: str("8Gi"), EphemeralStorage: str("8Gi")}}
			b := &repositoryTask{nodeTask: tasks[1], defaults: defaults}
			workers := []jobworkflow.TaskWorker{tasks[0], b}
			alloc := execution.Allocation{SchemaVersion: execution.SchemaVersion, Base: "strong", Capabilities: []string{"strong"}, Resources: execution.Resources{CPU: str("1"), Memory: str("1Gi"), EphemeralStorage: str("1Gi")}}
			out, event := runNode(t, f, alloc, workers)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.NotNil(t, event)
			expected := execution.Overlay(defaults, strong)
			require.Equal(t, expected, event.Demand.Effective)
			require.Zero(t, b.runs.Load())
			alloc.Capabilities = append(alloc.Capabilities, "repo")
			out, event = runNode(t, f, alloc, workers)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.Nil(t, event)
			require.EqualValues(t, 1, tasks[0].runs.Load())
			require.EqualValues(t, 1, b.runs.Load())
		})
	}
}

func TestRepositoryDefaultsCombinedRecipeNodeScopes(t *testing.T) {
	f, tasks := newNodeFixture(t, "toy", `id: combined-scopes
execution_needs:
 base: recipe
 capabilities: [outer]
 resources: {cpu: 4, memory: 4Gi}
sequence:
 - op: needs_a
   execution_needs:
    base: node
    capabilities: [inner]
    resources: {memory: 1Gi, ephemeral-storage: 2Gi}
 - op: needs_b
   execution_needs:
    capabilities: []
    resources: {ephemeral-storage: 2Gi}
`)
	defaults := execution.Requirements{Base: str("repo"), Capabilities: []string{"repo"}, Resources: execution.Resources{CPU: str("8"), Memory: str("8Gi"), EphemeralStorage: str("8Gi")}}
	a := &repositoryTask{nodeTask: tasks[0], defaults: defaults}
	b := &repositoryTask{nodeTask: tasks[1], defaults: defaults}
	alloc := execution.Allocation{SchemaVersion: execution.SchemaVersion, Base: "node", Capabilities: []string{"outer", "inner"}, Resources: execution.Resources{CPU: str("4"), Memory: str("1Gi"), EphemeralStorage: str("2Gi")}}
	out, event := runNode(t, f, alloc, []jobworkflow.TaskWorker{a, b})
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.EqualValues(t, 1, a.runs.Load(), "all node/recipe scalars replace larger defaults")
	require.Zero(t, b.runs.Load())
	require.Equal(t, "recipe", *event.Demand.Effective.Base)
	require.Equal(t, "4Gi", *event.Demand.Effective.Resources.Memory)
	require.Equal(t, []string{"outer"}, event.Demand.Effective.Capabilities, "empty node retains enclosing capabilities")
	alloc.Base = "recipe"
	alloc.Resources.Memory = str("4Gi")
	out, event = runNode(t, f, alloc, []jobworkflow.TaskWorker{a, b})
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.EqualValues(t, 1, b.runs.Load())
}

func TestRepositoryDefaultsNodeCapabilityPresence(t *testing.T) {
	for _, capabilities := range []string{"", "   execution_needs: {capabilities: []}\n", "   execution_needs: {capabilities: [node]}\n"} {
		t.Run(capabilities, func(t *testing.T) {
			f, tasks := newNodeFixture(t, "toy", "id: node-presence\nsequence:\n - op: needs_a\n"+capabilities)
			a := &repositoryTask{nodeTask: tasks[0], defaults: execution.Requirements{Capabilities: []string{"repo"}}}
			out, event := runNode(t, f, execution.Allocation{SchemaVersion: execution.SchemaVersion, Capabilities: []string{"node"}}, []jobworkflow.TaskWorker{a})
			if capabilities == "" {
				require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
				require.Equal(t, []string{"repo"}, event.Demand.Effective.Capabilities)
				require.Zero(t, a.runs.Load())
			} else {
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
				require.Nil(t, event)
				require.EqualValues(t, 1, a.runs.Load())
			}
		})
	}
}
