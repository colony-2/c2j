package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestPythonRecipeScopesAndReplay(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv required")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python required")
	}
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	t.Setenv("UV_CACHE_DIR", t.TempDir())
	t.Setenv("UV_PYTHON_DOWNLOADS", "never")
	t.Setenv("UV_NO_INDEX", "true")
	type input struct{}
	type output struct {
		Prefix string `json:"prefix"`
	}
	op := coreops.NewActivityMappedOpV2[input, output](coreops.OpMetadata{Type: "python_probe"}, func(_ coreops.OpDependencies, ctx context.Context, _ input) (output, error) {
		raw, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{Command: []string{"python3", "-c", `import json, sys; print(json.dumps({'prefix': sys.prefix}))`}})
		if err != nil {
			return output{}, fmt.Errorf("probe: %w: %s", err, stderr)
		}
		var out output
		err = json.Unmarshal(raw, &out)
		return out, err
	})
	withRegisteredOps(t, op, extops.GetExecutionOp())
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), registry)
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".c2"), 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "extension"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".c2/config.yaml"), []byte("execution:\n  python: {requires-python: '>=3.8', dependencies: []}\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "extension/op.yaml"), []byte(`name: probe
input_schema: {type: object}
output_schema: {type: object}
python: {requires-python: '>=3.8', dependencies: []}
command: [python3, '-c', 'import json, sys; print(json.dumps({"output": {"prefix": sys.prefix}}))']
`), 0600))
	// The first/last nodes inherit repository Python; the nested block replaces it.
	// An unused invalid declaration must never invoke uv during preparation.
	rec, err := recipe.LoadRecipeFromString([]byte(`id: python-scopes
sequence:
  - id: first
    op: python_probe
  - id: nested
    execution_needs:
      python: {requires-python: '>=3.9', dependencies: []}
    sequence:
      - id: inside
        op: python_probe
      - id: extension
        op: ./extension
    outputs:
      prefix: '${{ sequence.inside.outputs.prefix }}'
      extension: '${{ sequence.extension.outputs.prefix }}'
  - id: last
    op: python_probe
  - id: skipped
    when: 'false'
    execution_needs:
      python: {requires-python: '>=99', dependencies: [invalid]}
    op: python_probe
outputs:
  first: '${{ sequence.first.outputs.prefix }}'
  nested: '${{ sequence.nested.outputs.prefix }}'
  extension: '${{ sequence.nested.outputs.extension }}'
  last: '${{ sequence.last.outputs.prefix }}'
`))
	require.NoError(t, err)
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}, {"add", "."}, {"commit", "-m", "python"}} {
		require.NoError(t, runGit(root, "git", args...))
	}
	commit := gitHead(t, root)
	job, gitctx := GenerateTestContext()
	job.GitBase.BaseRepo = root
	job.GitBase.BaseRef = commit
	job.GitBase.ResolvedBaseHash = commit
	gitctx.ParentRef = commit
	engine := newToyEngineWithWorkSet(t, "python", ws, nil)
	celltest.Create(t, engine, "python", "test", celltest.Repo(root))
	ref, err := starter.StartRecipeJob(celltest.Context(context.Background()), workflowctl.StartJob{TenantId: "python", RecipeName: "python-scopes", JobContext: job, GitRef: commit, Cell: "test", CellRepo: celltest.Repo(root)}, engine, *rec)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(celltest.Context(context.Background()), 20*time.Second, ref.Key(), engine))
	result, err := swfutil.JobResult(celltest.Context(context.Background()), engine, ref.Key())
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	var out map[string]string
	require.NoError(t, json.Unmarshal(raw, &out))
	require.NotEmpty(t, out["first"])
	require.Equal(t, out["first"], out["last"])
	require.Equal(t, out["first"], out["extension"])
	require.NotEqual(t, out["first"], out["nested"])
	run, err := engine.GetJobRun(celltest.Context(context.Background()), jobdb.GetJobRunRequest{JobKey: ref.Key(), IncludeOutputs: true})
	require.NoError(t, err)
	outcomes := map[string]int{}
	for _, task := range executionTaskRuns(run) {
		decoded, err := decodeTaskOutput(task.Attempts[0].Output.Data)
		require.NoError(t, err)
		require.NotNil(t, decoded.Activity.Setup.Python)
		outcomes[decoded.Activity.Setup.Python.Outcome]++
	}
	require.Equal(t, map[string]int{"prepared": 2, "reused": 2}, outcomes)
	require.NoError(t, os.RemoveAll(os.Getenv("C2J_TOOL_CACHE_DIR")))
	require.NoError(t, os.RemoveAll(root))
	replay, err := engine.ReplayJobRun(celltest.Context(context.Background()), jobworkflow.ReplayRunRequest{JobKey: ref.Key(), JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
	require.NoError(t, err)
	replayRaw, err := replay.GetData()
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(replayRaw))
	require.NoDirExists(t, os.Getenv("C2J_TOOL_CACHE_DIR"))
}
