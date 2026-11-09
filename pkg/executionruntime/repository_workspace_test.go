package executionruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRepositoryDefaultsPersistedEditsAndWorkspaces(t *testing.T) {
	ctx := celltest.Context(context.Background())
	write := func(root, base, memory string) error {
		return os.WriteFile(filepath.Join(root, ".c2/config.yaml"), []byte("execution: {base: "+base+", resources: {memory: "+memory+"}}"), 0600)
	}
	repos := map[string]string{"A": t.TempDir(), "B": t.TempDir()}
	for name, root := range repos {
		require.NoError(t, os.Mkdir(filepath.Join(root, ".c2"), 0700))
		memory := "1Gi"
		if name == "B" {
			memory = "8Gi"
		}
		require.NoError(t, write(root, name, memory))
		for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.com"}, {"add", "."}, {"commit", "-m", "config"}} {
			out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
			require.NoError(t, err, string(out))
		}
	}
	base := toy.New()
	for name, root := range repos {
		celltest.Create(t, base, "tenant", name, celltest.Repo(root))
	}
	calls := 0
	op := coreops.NewActivityMappedOpV2[struct {
		Edit bool `json:"edit"`
	}, map[string]any](coreops.OpMetadata{Type: "repository_workspace_probe"}, func(deps coreops.OpDependencies, ctx context.Context, in struct {
		Edit bool `json:"edit"`
	}) (map[string]any, error) {
		calls++
		if in.Edit {
			return nil, write(deps.WorktreePath(), "A", "4Gi")
		}
		return map[string]any{"ok": true}, nil
	})
	coreops.Register(op)
	reg, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(base).BuildEngine()
	require.NoError(t, err)
	r, err := recipe.LoadRecipeFromString([]byte(`id: repository-workspaces
execution_needs: {resources: {cpu: 1}}
sequence:
 - id: edit
   op: repository_workspace_probe
   inputs: {edit: true}
 - id: updated
   op: repository_workspace_probe
 - id: scoped
   workspace: {cell: B}
   execution_needs: {base: A, resources: {memory: 1Gi}}
   op: repository_workspace_probe
 - id: other
   workspace: {cell: B}
   op: repository_workspace_probe
 - id: back
   op: repository_workspace_probe
`))
	require.NoError(t, err)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repos["A"], BaseRef: "main"}}
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", RecipeName: "repository-workspaces", JobContext: job, GitRef: "main", Cell: "A", CellRepo: celltest.Repo(repos["A"])}, jobdbschema.WorkflowEngine{Engine: engine, Registry: base}, *r)
	require.NoError(t, err)
	run := func(baseName, memory string) (jobworkflow.JobRunOutcome, *compiler.ExecutionHandoff) {
		alloc := execution.Allocation{SchemaVersion: execution.SchemaVersion, Base: baseName, Resources: execution.Resources{CPU: str("1"), Memory: str(memory)}}
		rt := New(base, alloc)
		ws, err := compiler.NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().Build(), reg, compiler.RecipeJobWorkerOptions{Allocation: alloc, StageExecution: rt.Stage, WrapTaskWorker: rt.WrapTaskWorker})
		require.NoError(t, err)
		var workers []jobworkflow.TaskWorker
		for _, w := range ws.TaskWorkers {
			workers = append(workers, w)
		}
		runnable, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: ref.Key(), JobWorker: ws.JobWorker, TaskWorkers: workers, WorkerID: "workspace-defaults", LeaseDuration: time.Minute})
		require.NoError(t, err)
		out, err := runnable.Run(nil)
		require.NoError(t, err)
		event, err := compiler.ExecutionHandoffFromOutcome(out)
		require.NoError(t, err)
		return out, event
	}
	for _, step := range []struct {
		base, memory, wantBase, wantMemory string
		calls                              int
	}{
		{"A", "1Gi", "A", "4Gi", 1},
		{"A", "4Gi", "B", "8Gi", 3},
		{"B", "8Gi", "A", "4Gi", 4},
	} {
		out, event := run(step.base, step.memory)
		require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
		require.NotNil(t, event)
		require.Equal(t, step.wantBase, *event.Demand.Effective.Base)
		require.Equal(t, step.wantMemory, *event.Demand.Effective.Resources.Memory)
		require.Equal(t, "1", *event.Demand.Effective.Resources.CPU)
		require.True(t, event.Demand.Overrides.Empty())
		require.Equal(t, step.calls, calls)
	}
	out, event := run("A", "4Gi")
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.Equal(t, 5, calls)
	for _, root := range repos {
		require.NoError(t, os.RemoveAll(root))
	}
	out, event = run("", "1Gi")
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.Equal(t, 5, calls)
}
