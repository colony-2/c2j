package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestLazyToolSetupScopeTimeoutAndReplay(t *testing.T) {
	bin := t.TempDir()
	cache := t.TempDir()
	log := filepath.Join(t.TempDir(), "install.log")
	t.Setenv("C2J_TOOL_CACHE_DIR", cache)
	t.Setenv("TOOL_INSTALL_LOG", log)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "uv"), []byte(`#!/bin/sh
set -eu
sleep 0.4
for spec do :; done
printf '%s\n' "$spec" >> "$TOOL_INSTALL_LOG"
mkdir -p "$UV_TOOL_BIN_DIR"
printf '#!/bin/sh\necho %s\n' "'$spec'" > "$UV_TOOL_BIN_DIR/tool"
chmod +x "$UV_TOOL_BIN_DIR/tool"
`), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	type input struct{}
	type output struct {
		Text string `json:"text"`
	}
	op := coreops.NewActivityMappedOpV2[input, output](coreops.OpMetadata{Type: "tool_probe"}, func(_ coreops.OpDependencies, ctx context.Context, _ input) (output, error) {
		out, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{Command: []string{"tool"}})
		if err != nil {
			return output{}, fmt.Errorf("tool: %w: %s", err, stderr)
		}
		return output{Text: strings.TrimSpace(string(out))}, nil
	})
	withRegisteredOps(t, op, extops.GetExecutionOp())
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), registry)
	require.NoError(t, err)
	opRoot := t.TempDir()
	opDir := filepath.Join(opRoot, "extension")
	require.NoError(t, os.MkdirAll(opDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(`name: extension
input_schema: {type: object}
output_schema: {type: object}
dependencies: [uv:tool==3]
run: 'printf ''{"output":{"text":"%s"}}'' "$(tool)"'
`), 0600))
	rec, err := recipe.LoadRecipeFromString([]byte(`id: tools
execution:
  packages: [uv:tool==1]
sequence:
  - id: first
    op: tool_probe
    timeout: 250ms
  - id: nested
    execution_needs:
      packages: [uv:tool==2]
    sequence:
      - id: inner
        op: tool_probe
        timeout: 250ms
    outputs: {text: "${{ sequence.inner.outputs.text }}"}
  - id: last
    op: tool_probe
  - id: extension
    op: ./extension
  - id: branches
    state:
      initial: idle
      states:
        idle:
          sequence: []
        unused:
          execution_needs:
            packages: [uv:never]
          op: git+file:///no-such-tool-source//ops/missing@HEAD
outputs:
  first: "${{ sequence.first.outputs.text }}"
  nested: "${{ sequence.nested.outputs.text }}"
  last: "${{ sequence.last.outputs.text }}"
  extension: "${{ sequence.extension.outputs.text }}"
`))
	require.NoError(t, err)
	job, git := GenerateTestContext()
	require.NoError(t, runGit(opRoot, "git", "init"))
	require.NoError(t, runGit(opRoot, "git", "config", "user.email", "test@example.com"))
	require.NoError(t, runGit(opRoot, "git", "config", "user.name", "Test"))
	require.NoError(t, runGit(opRoot, "git", "add", "."))
	require.NoError(t, runGit(opRoot, "git", "commit", "-m", "tools"))
	commit := gitHead(t, opRoot)
	job.GitBase.BaseRepo = opRoot
	job.GitBase.BaseRef = commit
	job.GitBase.ResolvedBaseHash = commit
	git.ParentRef = commit
	engine := newToyEngineWithWorkSet(t, "tools", ws, nil)
	key, err := starter.StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "tools", RecipeName: "tools", JobContext: job, GitRef: git.ParentRef}, engine, *rec)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 10*time.Second, key, engine))
	result, err := swfutil.JobResult(context.Background(), engine, key)
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, map[string]any{"first": "tool==1", "nested": "tool==2", "last": "tool==1", "extension": "tool==3"}, out)
	installs, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, "tool==1\ntool==2\ntool==3\n", string(installs))
	run, err := engine.GetJobRun(context.Background(), jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true})
	require.NoError(t, err)
	setups := 0
	for _, task := range run.Attempts[0].Tasks {
		if task.TaskType != workerops.ToolSetupTaskType {
			continue
		}
		setups++
		var setup workerops.ToolSetupResult
		require.NoError(t, json.Unmarshal(task.Attempts[0].Output.Data, &setup))
		if setups == 1 {
			require.GreaterOrEqual(t, setup.Diagnostics.WallMS, int64(350))
		}
	}
	require.Equal(t, 4, setups)
	// Replay must work after both installed tools and the extension source disappear.
	require.NoError(t, os.RemoveAll(cache))
	require.NoError(t, os.RemoveAll(opRoot))
	require.NoError(t, os.RemoveAll(bin))
	replay, err := engine.ReplayJobRun(context.Background(), jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
	require.NoError(t, err)
	replayRaw, err := replay.GetData()
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(replayRaw))
	after, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, string(installs), string(after))
}

type loseToolsBeforeStep struct {
	jobworkflow.TaskWorker
	cache string
	lost  *bool
}

func (w loseToolsBeforeStep) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	if !*w.lost {
		*w.lost = true
		if err := os.RemoveAll(w.cache); err != nil {
			return nil, err
		}
	}
	return w.TaskWorker.Run(ctx, input)
}

func TestToolRecoveryResumesCurrentStepOutsideOpTimeout(t *testing.T) {
	bin := t.TempDir()
	cache := t.TempDir()
	t.Setenv("C2J_TOOL_CACHE_DIR", cache)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "uv"), []byte(`#!/bin/sh
set -eu
sleep 0.4
mkdir -p "$UV_TOOL_BIN_DIR"
printf '#!/bin/sh\necho ready\n' > "$UV_TOOL_BIN_DIR/tool"
chmod +x "$UV_TOOL_BIN_DIR/tool"
`), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	first, second := 0, 0
	type chainData struct {
		Previous string `json:"previous,omitempty"`
		Done     bool   `json:"done,omitempty"`
	}
	probe := func(ctx context.Context) error {
		_, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{Command: []string{"tool"}})
		if err != nil {
			return fmt.Errorf("%w: %s", err, stderr)
		}
		return nil
	}
	op, err := coreops.NewOp().WithType("tool_chain").
		AddStep("first", coreops.NewStepWithDeps(func(_ coreops.OpDependencies, ctx context.Context, in chainData) (chainData, error) {
			first++
			return chainData{Previous: "first"}, probe(ctx)
		})).
		AddStep("second", coreops.NewStepWithDeps(func(_ coreops.OpDependencies, ctx context.Context, in chainData) (chainData, error) {
			second++
			if in.Previous != "first" {
				return chainData{}, fmt.Errorf("lost previous step output")
			}
			return chainData{Done: true}, probe(ctx)
		})).Build()
	require.NoError(t, err)
	withRegisteredOps(t, op.(coreops.RegisterableOp))
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	lost := false
	ws, err := NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().Build(), registry, RecipeJobWorkerOptions{WrapTaskWorker: func(worker jobworkflow.TaskWorker) jobworkflow.TaskWorker {
		if worker.Name() == "tool_chain:second" {
			return loseToolsBeforeStep{TaskWorker: worker, cache: cache, lost: &lost}
		}
		return worker
	}})
	require.NoError(t, err)
	rec, err := recipe.LoadRecipeFromString([]byte(`id: recovery
execution: {packages: [uv:tool==1]}
sequence:
 - id: work
   op: tool_chain
   timeout: 250ms
`))
	require.NoError(t, err)
	job, git := GenerateTestContext()
	engine := newToyEngineWithWorkSet(t, "tools", ws, nil)
	key, err := starter.StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "tools", RecipeName: "recovery", JobContext: job, GitRef: git.ParentRef}, engine, *rec)
	require.NoError(t, err)
	waitErr := jobworkflow.WaitForJobToComplete(context.Background(), 10*time.Second, key, engine)
	require.NoError(t, waitErr)
	_, err = swfutil.JobResult(context.Background(), engine, key)
	require.NoError(t, err)
	require.True(t, lost)
	require.Equal(t, 1, first, "completed first step must never be repeated")
	require.Equal(t, 1, second)
	require.NoError(t, os.RemoveAll(cache))
	require.NoError(t, os.RemoveAll(bin))
	_, err = engine.ReplayJobRun(context.Background(), jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
	require.NoError(t, err)
	require.Equal(t, 1, first)
	require.Equal(t, 1, second)
}
