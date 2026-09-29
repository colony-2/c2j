package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/objects"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type objectProbeInput struct {
	Session  *objects.Ref `json:"session,omitempty"`
	Write    string       `json:"write"`
	Expected string       `json:"expected"`
	FailOnce bool         `json:"fail_once,omitempty"`
}

func objectProbe(t *testing.T, observer ...func(objectProbeInput)) coreops.RegisterableOp {
	var failed sync.Map
	return coreops.NewActivityMappedOpV2[objectProbeInput, map[string]any](coreops.OpMetadata{Type: "object_probe"}, func(deps coreops.OpDependencies, ctx context.Context, in objectProbeInput) (map[string]any, error) {
		if len(observer) > 0 {
			observer[0](in)
		}
		for _, a := range deps.GetInputArtifacts() {
			if objects.IsInternalArtifact(a.Name()) {
				return nil, fmt.Errorf("object archive leaked into op artifacts")
			}
		}
		home, err := os.MkdirTemp("", "object-probe-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(home)
		if in.Session != nil {
			snap, err := deps.Objects().Open(ctx, *in.Session, "test.session/v1")
			if err != nil {
				return nil, err
			}
			defer snap.Close()
			home = snap.Files["home"]
			data, err := os.ReadFile(filepath.Join(home, "rollout"))
			if err != nil {
				return nil, err
			}
			if string(data) != in.Expected {
				return nil, fmt.Errorf("checkpoint corrupted: got %q expected %q", data, in.Expected)
			}
		}
		if err := os.WriteFile(filepath.Join(home, "rollout"), []byte(in.Write), 0600); err != nil {
			return nil, err
		}
		if in.FailOnce {
			if _, seen := failed.LoadOrStore(deps.JobTool().GetJobKey().JobId+in.Write, true); !seen {
				return nil, fmt.Errorf("simulated failure after modifying private checkpoint")
			}
		}
		ref, err := deps.Objects().Publish(ctx, "test.session/v1", map[string]any{"session_id": "same-id"}, map[string]string{"home": home})
		if err != nil {
			return nil, err
		}

		sealed, err := deps.Objects().Open(ctx, ref, ref.Type)
		if err != nil {
			return nil, err
		}
		defer sealed.Close()
		data, err := os.ReadFile(filepath.Join(sealed.Files["home"], "rollout"))
		if err != nil {
			return nil, err
		}
		if string(data) != in.Write {
			return nil, fmt.Errorf("new checkpoint did not freeze the current invocation")
		}
		return map[string]any{"session": ref}, nil
	})
}
func TestObjectCheckpointRecipeBranchesAndReplay(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "A")
	var seedCalls atomic.Int64
	workspaceTestWorker(t, objectProbe(t, func(in objectProbeInput) {
		if in.Write == "original" {
			seedCalls.Add(1)
		}
	}))
	eng := objectEngine(t)
	out := runObjectRecipe(t, eng, `id: workspace-test
version: "1"
sequence:
 - id: first
   op: object_probe
   inputs: {write: original}
 - id: second
   op: object_probe
   retry: {maximum_attempts: 2, initial_interval: 1ms}
   inputs:
     fail_once: true
     session: "${{ sequence.first.outputs.session }}"
     expected: original
     write: second
 - id: third
   op: object_probe
   inputs:
     session: "${{ sequence.first.outputs.session }}"
     expected: original
     write: third
 - id: fourth
   op: object_probe
   inputs:
     session: "${{ sequence.second.outputs.session }}"
     expected: second
     write: fourth
outputs:
 original: "${{ sequence.first.outputs.session }}"
 second: "${{ sequence.second.outputs.session }}"
 third: "${{ sequence.third.outputs.session }}"
 public_artifacts: "${{ sequence.first.artifacts }}"
`, contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}})
	require.EqualValues(t, 1, seedCalls.Load(), "recipe replay must reuse the original task outcome")
	first, ok, err := objects.Parse(out["original"])
	require.NoError(t, err)
	require.True(t, ok)
	second, ok, err := objects.Parse(out["second"])
	require.NoError(t, err)
	require.True(t, ok)
	third, ok, err := objects.Parse(out["third"])
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEqual(t, first.SHA256, second.SHA256)
	require.NotEqual(t, first.SHA256, third.SHA256)
	for name := range out["public_artifacts"].(map[string]any) {
		require.False(t, objects.IsInternalArtifact(name))
	}
}

func objectEngine(t *testing.T) jobworkflow.Engine {
	eng, _ := objectEngineAt(t, filepath.Join(t.TempDir(), "jobs.db"))
	return eng
}
func objectEngineAt(t *testing.T, dbPath string) (jobworkflow.Engine, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: dbPath})
	require.NoError(t, err)
	eng, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("object-tests").BuildEngine()
	require.NoError(t, err)
	eng = jobdbschema.WorkflowEngine{Engine: eng, Registry: rt}
	ctl := &workerworkflow.SWFWorkflowControl{Engine: eng}
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(ctl).Build()
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ws, err := NewRecipeWorkerWithOptions(deps, registry, RecipeJobWorkerOptions{RootSourceResolver: NewRecipeSourceResolver(RecipeSourceResolverOptions{})})
	require.NoError(t, err)
	require.NoError(t, eng.RegisterWorkers(ws))
	done := make(chan struct{})
	go func() { defer close(done); eng.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("engine failed to stop")
			}
			require.NoError(t, rt.Close(context.Background()))
		})
	}
	t.Cleanup(stop)
	return eng, stop
}

func runObjectRecipe(t *testing.T, eng jobworkflow.Engine, source string, job contextual.JobContext, inputs ...map[string]any) map[string]any {
	t.Helper()
	rec, err := recipe.LoadRecipeFromString([]byte(source))
	require.NoError(t, err)
	return runObjectRecipeValue(t, eng, rec, job, inputs...)
}
func runObjectRecipeValue(t *testing.T, eng jobworkflow.Engine, rec *recipe.Recipe, job contextual.JobContext, inputs ...map[string]any) map[string]any {
	t.Helper()
	key, err := starter.StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "object-tests", RecipeName: rec.GetMetadata().ID, Inputs: firstObjectInputs(inputs), JobContext: job, GitRef: job.GitBase.BaseRef}, eng, *rec)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 15*time.Second, key, eng))
	result, err := swfutil.JobResult(context.Background(), eng, key)
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func TestObjectCheckpointsAcrossParallelChildJobsAndLaterJob(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "A")
	workspaceTestWorker(t, objectProbe(t))
	child := `id: child
version: "1"
input_schema:
 session: {type: object, object_type: test.session/v1, required: true}
 write: {type: string, required: true}
inputs:
 session: "${{ inputs.session }}"
 write: "${{ inputs.write }}"
sequence:
 - id: resume
   op: object_probe
   inputs:
    session: "${{ inputs.session }}"
    expected: original
    write: "${{ inputs.write }}"
outputs:
 session: "${{ sequence.resume.outputs.session }}"
`
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".c2j", "recipes"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".c2j", "recipes", "child.yaml"), []byte(child), 0644))
	require.NoError(t, runGit(repo, "git", "add", "."))
	require.NoError(t, runGit(repo, "git", "commit", "-m", "child recipe"))
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	eng, stop := objectEngineAt(t, dbPath)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	out := runObjectRecipe(t, eng, `id: workspace-test
version: "1"
sequence:
 - id: first
   op: object_probe
   inputs: {write: original}
 - id: branches
   child_group:
    mode: run_and_get_result
    children:
     - key: left
       recipe: child
       inputs:
        session: "${{ sequence.first.outputs.session }}"
        write: left
     - key: right
       recipe: child
       inputs:
        session: "${{ sequence.first.outputs.session }}"
        write: right
 - id: read_left
   op: object_probe
   inputs:
    session: "${{ sequence.branches.outputs.children[0].outputs.session }}"
    expected: left
    write: left_updated
 - id: read_right
   op: object_probe
   inputs:
    session: "${{ sequence.branches.outputs.children[1].outputs.session }}"
    expected: right
    write: right_updated
outputs:
 session: "${{ sequence.first.outputs.session }}"
 children: "${{ sequence.branches.outputs.children }}"
`, job)
	for _, raw := range out["children"].([]any) {
		child := raw.(map[string]any)
		require.Equal(t, "completed", child["status"])
		if arts, ok := child["artifacts"].(map[string]any); ok {
			for name := range arts {
				require.False(t, objects.IsInternalArtifact(name))
			}
		}
	}
	stop()
	eng, _ = objectEngineAt(t, dbPath)
	later := runObjectRecipe(t, eng, `id: workspace-test
version: "1"
input_schema:
 session: {type: object, object_type: test.session/v1, required: true}
inputs:
 session: "${{ inputs.session }}"
sequence:
 - id: resume
   op: object_probe
   inputs:
    session: "${{ inputs.session }}"
    expected: original
    write: independent_job
outputs:
 session: "${{ sequence.resume.outputs.session }}"
`, job, map[string]any{"session": out["session"]})
	require.NotEqual(t, out["session"], later["session"])
}

func firstObjectInputs(inputs []map[string]any) map[string]any {
	if len(inputs) > 0 {
		return inputs[0]
	}
	return nil
}

func TestExtensionObjectCheckpointThroughIncludedStateRecipe(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "A")
	require.NoError(t, os.Mkdir(filepath.Join(repo, "session"), 0755))
	for _, name := range []string{"op.yaml", "run.py"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "ops", "extensions", "testdata", "object-session", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(repo, "session", name), data, 0644))
	}
	phase := `id: resume-phase
version: "1"
input_schema:
 session: {type: object, object_type: test.session/v1, required: true}
inputs:
 session: "${{ inputs.session }}"
sequence:
 - id: resume
   op: ./session
   inputs:
    session: "${{ inputs.session }}"
    expected: original
    text: resumed
outputs:
 session: "${{ sequence.resume.outputs.session }}"
`
	require.NoError(t, os.WriteFile(filepath.Join(repo, "phase.yaml"), []byte(phase), 0644))
	require.NoError(t, runGit(repo, "git", "add", "."))
	require.NoError(t, runGit(repo, "git", "commit", "-m", "extension session fixture"))
	workspaceTestWorker(t, extensions.GetExecutionOp())
	eng := objectEngine(t)
	root := `id: workspace-test
version: "1"
sequence:
 - id: start
   op: ./session
   inputs: {text: original}
 - id: via_include
   include: ./phase.yaml
   inputs:
    session: "${{ sequence.start.outputs.session }}"
 - id: state_resume
   inputs:
    session: "${{ sequence.start.outputs.session }}"
   state:
    initial: continue
    states:
     continue:
      op: ./session
      inputs:
       session: "${{ inputs.session }}"
       expected: original
       text: from_state
   outputs:
    session: "${{ states.continue.outputs.session }}"
 - id: read_include
   op: ./session
   inputs:
    session: "${{ sequence.via_include.outputs.session }}"
    expected: resumed
    text: done
outputs:
 session: "${{ sequence.state_resume.outputs.session }}"
`
	parsed, err := recipe.LoadRecipeFromString([]byte(root))
	require.NoError(t, err)
	expanded, err := ResolveInlineRecipes(context.Background(), *parsed, InlineResolutionOptions{RootFile: filepath.Join(repo, "root.yaml")})
	require.NoError(t, err)
	out := runObjectRecipeValue(t, eng, &expanded.Recipe, contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}})
	ref, ok, err := objects.Parse(out["session"])
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "test.session/v1", ref.Type)
}
