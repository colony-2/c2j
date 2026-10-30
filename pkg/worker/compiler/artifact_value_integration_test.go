package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestValueArtifactNativeRetryAndExport(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "A")
	type input struct {
		Inbox    string        `json:"inbox"`
		Document artifacts.Ref `json:"document"`
	}
	var calls atomic.Int32
	probe := coreops.NewActivityMappedOpV2[input, map[string]any](coreops.OpMetadata{Type: "value_probe", AcceptsArtifacts: true}, func(_ coreops.OpDependencies, _ context.Context, in input) (map[string]any, error) {
		got, err := os.ReadFile(filepath.Join(in.Inbox, in.Document.Name))
		if err != nil {
			return nil, err
		}
		want, err := in.Document.ValueBytes()
		if err != nil {
			return nil, err
		}
		if string(got) != string(want) || len(got) != 524288 {
			return nil, fmt.Errorf("unexpected file contents (%d bytes)", len(got))
		}
		if calls.Add(1) == 1 {
			return nil, fmt.Errorf("retry after reading the file")
		}
		return map[string]any{"size": len(got)}, nil
	})
	workspaceTestWorker(t, probe)
	db := filepath.Join(t.TempDir(), "jobs.db")
	eng, stop := objectEngineAt(t, db)
	rec, err := recipe.LoadRecipeFromString([]byte(`id: root
sequence:
 - id: consume
   op: value_probe
   retry: {maximum_attempts: 2}
   inputs:
    inbox: '${{ context.environment.inbox }}'
    document:
     kind: value
     name: context.txt
     format: text
     value: '${{ jq(0, "\"x\" * 524288") }}'
outputs:
 size: '${{ sequence.consume.outputs.size }}'
 artifact_refs:
  result.txt:
   kind: value
   format: text
   value: '${{ jq(0, "\"y\" * 524288") }}'
`))
	require.NoError(t, err)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	keyRef, err := starter.StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "object-tests", RecipeName: "root", JobContext: job, GitRef: "main"}, eng, *rec)
	require.NoError(t, err)
	key := keyRef.Key()
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 30*time.Second, key, eng))
	stop()
	// Reopen the database and replay with a replacement engine. Completed ops
	// must not execute, and the exported artifact must still be readable.
	eng, _ = objectEngineAt(t, db)
	_, err = eng.ReplayJobRun(context.Background(), jobworkflow.ReplayRunRequest{JobKey: key})
	require.NoError(t, err)
	result, err := swfutil.JobResult(context.Background(), eng, key)
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	require.Less(t, len(raw), 4096)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, float64(524288), out["size"])
	attached, err := result.GetArtifacts()
	require.NoError(t, err)
	found := false
	for _, a := range attached {
		if a.Name() == "result.txt" {
			stored, err := a.ArtifactKey()
			require.NoError(t, err)
			require.Equal(t, key.JobId, stored.JobId)
			b, err := a.Bytes(context.Background())
			require.NoError(t, err)
			require.Equal(t, strings.Repeat("y", 524288), string(b))
			found = true
		}
	}
	require.True(t, found)
	require.EqualValues(t, 2, calls.Load())
}

func TestValueArtifactPackagedOpThroughInclude(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "A")
	require.NoError(t, os.Mkdir(filepath.Join(repo, "inspect"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "inspect", "op.yaml"), []byte(`name: inspect
command: [python3, run.py]
input_schema:
 type: object
 properties:
  path: {type: string}
output_schema:
 type: object
 properties:
  size: {type: integer}
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "inspect", "run.py"), []byte(`import json, os, sys
request = json.load(sys.stdin)
with open(request["path"], "rb") as f:
    content = f.read()
assert content == b"x" * 2097152
assert "x" * 1000 not in str(sys.argv) + str(dict(os.environ))
print(json.dumps({"output": {"size": len(content)}}))
`), 0644))
	phase := `id: phase
input_schema:
 documents: {type: artifact_map, required: true}
inputs:
 documents: '${{ inputs.documents }}'
sequence:
 - id: consume
   op: ./inspect
   artifacts:
    docs/: '${{ inputs.documents }}'
   inputs:
    path: '${{ context.environment.op.inbox }}/docs/context.txt'
outputs:
 size: '${{ sequence.consume.outputs.size }}'
 artifact_refs:
  forwarded.txt: '${{ inputs.documents["context.txt"] }}'
`
	require.NoError(t, os.WriteFile(filepath.Join(repo, "phase.yaml"), []byte(phase), 0644))
	require.NoError(t, runGit(repo, "git", "add", "."))
	require.NoError(t, runGit(repo, "git", "commit", "-m", "value artifact fixture"))
	workspaceTestWorker(t, extensions.GetExecutionOp())
	root := `id: root
sequence:
 - id: helper
   include: ./phase.yaml
   perms: {limit: {jobdb: {}}}
   inputs:
    documents:
     context.txt:
      kind: value
      format: text
      value: '${{ jq(0, "\"x\" * 2097152") }}'
 - id: again
   op: ./inspect
   artifacts:
    context.txt: '${{ sequence.helper.artifacts["forwarded.txt"] }}'
   inputs:
    path: '${{ context.environment.op.inbox }}/context.txt'
outputs:
 size: '${{ sequence.again.outputs.size }}'
`
	parsed, err := recipe.LoadRecipeFromString([]byte(root))
	require.NoError(t, err)
	expanded, err := ResolveInlineRecipes(context.Background(), *parsed, InlineResolutionOptions{RootFile: filepath.Join(repo, "root.yaml")})
	require.NoError(t, err)
	out := runObjectRecipeValue(t, objectEngine(t), &expanded.Recipe, contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}})
	require.Equal(t, float64(2097152), out["size"])
}

func TestValueArtifactGuide(t *testing.T) {
	guide, err := os.ReadFile(filepath.Join("..", "..", "artifacts", "guides", "VALUES.md"))
	require.NoError(t, err)
	source := strings.SplitN(strings.SplitN(string(guide), "```yaml\n", 2)[1], "```", 2)[0]
	rec, err := recipe.LoadRecipeFromString([]byte(source))
	require.NoError(t, err)
	workspaceTestWorker(t, commandop.GetOp())
	job, git := GenerateTestContext()
	inner := &countingJobContext{jobKey: jobdb.JobKey{TenantId: "test", JobId: "validate"}}
	inputs := map[string]any{"document": " λ\n${{ untouched }}\n$(false) "}
	_, attached, err := ExecuteRecipe(newWorkflowContext(inner), *rec, inputs, job, git, ExecutionOptions{Mode: ExecutionModeValidate})
	require.NoError(t, err)
	require.Empty(t, attached)
	require.Zero(t, inner.calls, "validation must not execute tasks or write files")
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "A")
	out := runObjectRecipeValue(t, objectEngine(t), rec, contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}, inputs)
	stdout := out["result"].(map[string]any)["stdout"]
	require.Equal(t, fmt.Sprint(len(inputs["document"].(string))), stdout)
}
