package compiler

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/workflow"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Run only with the pre-change hooks.go; see the fixture README. These are real
// SQLite histories from the old compiler, not fabricated hook results.
func TestGenerateAbsentHookLegacyFixtures(t *testing.T) {
	dir := os.Getenv("C2J_GENERATE_ABSENT_HOOK_FIXTURES")
	if dir == "" {
		t.Skip("fixture generation is explicit and requires the legacy compiler")
	}
	require.NoError(t, os.MkdirAll(dir, 0755))
	for _, state := range []bool{false, true} {
		name := "sequence"
		if state {
			name = "state"
		}
		t.Run(name, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "source")
			workspaceRepo(t, repo, "A")
			workspaceTestWorker(t)
			path := filepath.Join(t.TempDir(), "jobs.db")
			rt, err := sqlite.NewFromConfig(testBackground(), sqlite.Config{DBPath: path, MaxInlineArtifactBytes: 10 << 20})
			require.NoError(t, err)
			call := ` - id: hook
   workspace: {cell: test}
   include:
    recipe: cell://.c2/hooks/check.yaml
    if_missing: {outputs: {status: skipped, cell: '${{ context.workspace.cell }}', hash: '${{ context.git.hash }}'}}
`
			tail := "sequence.hook"
			if state {
				call = ` - id: machine
   state:
    initial: hook
    states:
     hook:
      workspace: {cell: test}
      include:
       recipe: cell://.c2/hooks/check.yaml
       if_missing: {outputs: {status: skipped, cell: '${{ context.workspace.cell }}', hash: '${{ context.git.hash }}'}}
   outputs: {result: '${{ states.hook.outputs }}'}
`
				tail = "sequence.machine.outputs.result"
			} else {
				tail += ".outputs"
			}
			yaml := `id: git-lifecycle
sequence:
 - id: publication
   op: workspace_probe
   inputs: {write: published}
` + call + ` - id: later
   workspace: {cell: test}
   op: workspace_probe
outputs:
 result: '${{ ` + tail + ` }}'
 later_scope: '${{ sequence.later.outputs.scope }}'
 later_head: '${{ sequence.later.outputs.head }}'
`
			key := submitGitLifecycle(t, rt, repo, yaml)
			out, err := runGitLifecycle(t, rt, key)
			require.NoError(t, err)
			expected := gitLifecycleOutput(t, out)
			run, err := jobdb.GetJobRun(testBackground(), rt, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			raw, err := json.Marshal(run)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "skip_absent_workspace", "generate using the unmodified compiler and worker")
			info, err := json.MarshalIndent(struct {
				Key    jobdb.JobKey
				Output map[string]any
			}{key, expected}, "", "  ")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, name+".json"), info, 0644))
			require.NoError(t, rt.Close(testBackground()))
			src, err := os.Open(path)
			require.NoError(t, err)
			defer src.Close()
			dst, err := os.Create(filepath.Join(dir, name+".db.gz"))
			require.NoError(t, err)
			zipped := gzip.NewWriter(dst)
			_, err = io.Copy(zipped, src)
			require.NoError(t, err)
			require.NoError(t, zipped.Close())
			require.NoError(t, dst.Close())
		})
	}
}

func TestHookAbsentLegacyWorkspaceReplay(t *testing.T) {
	workspaceTestWorker(t) // Register op metadata; replay has no task workers.
	for _, name := range []string{"sequence", "state"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jobs.db")
			fixture, err := os.Open(filepath.Join("testdata/absent-hook-workspace", name+".db.gz"))
			require.NoError(t, err)
			defer fixture.Close()
			unzip, err := gzip.NewReader(fixture)
			require.NoError(t, err)
			defer unzip.Close()
			dst, err := os.Create(path)
			require.NoError(t, err)
			_, err = io.Copy(dst, unzip)
			require.NoError(t, err)
			require.NoError(t, dst.Close())
			raw, err := os.ReadFile(filepath.Join("testdata/absent-hook-workspace", name+".json"))
			require.NoError(t, err)
			var expected struct {
				Key    jobdb.JobKey
				Output map[string]any
			}
			require.NoError(t, json.Unmarshal(raw, &expected))
			rt, err := sqlite.NewFromConfig(testBackground(), sqlite.Config{DBPath: path, MaxInlineArtifactBytes: 10 << 20})
			require.NoError(t, err)
			defer rt.Close(testBackground())
			ctx := celltest.Context(testBackground())
			before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: expected.Key})
			require.NoError(t, err)
			// No task workers or live repositories are available. A rebuilt load or
			// workspace input cannot be mistaken for a successful execution.
			worker := NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})
			eng, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(expected.Key.TenantId).PlusWorkers(worker).BuildEngine()
			require.NoError(t, err)
			replayed, err := eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: expected.Key})
			require.NoError(t, err)
			var out map[string]any
			require.NoError(t, json.Unmarshal(replayed.GetDataOrPanic(), &out))
			require.Equal(t, expected.Output, out)
			after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: expected.Key})
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func absentReplayRecipe(state bool) string {
	call := ` - id: hook
   workspace: {cell: forbidden}
   include:
    recipe: cell://.c2/hooks/check.yaml
    if_missing: {outputs: {status: skipped}}
`
	value := "sequence.hook.outputs"
	if state {
		call = ` - id: machine
   state:
    initial: hook
    states:
     hook:
      workspace: {cell: forbidden}
      include:
       recipe: cell://.c2/hooks/check.yaml
       if_missing: {outputs: {status: skipped}}
   outputs: {result: '${{ states.hook.outputs }}'}
`
		value = "sequence.machine.outputs.result"
	}
	return `id: git-lifecycle
sequence:
 - id: publication
   op: absent_publication
` + call + ` - id: later
   workspace: {cell: test}
   op: workspace_probe
outputs:
 result: '${{ ` + value + ` }}'
 later_scope: '${{ sequence.later.outputs.scope }}'
 later_head: '${{ sequence.later.outputs.head }}'
`
}

func TestHookAbsentResumeAndRestart(t *testing.T) {
	for _, state := range []bool{false, true} {
		for _, frontier := range []string{"publication", "pin", "load", "later"} {
			t.Run(fmt.Sprintf("state=%t/%s", state, frontier), func(t *testing.T) {
				repo := filepath.Join(t.TempDir(), "repo")
				initial := workspaceRepo(t, repo, "A")
				publications := 0
				publication := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "absent_publication"}, func(_ coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
					publications++
					return map[string]any{"published": true}, nil
				})
				workspaceTestWorker(t, publication)
				rt := newTimeoutReproRuntime(t, "sqlite")
				key := submitGitLifecycle(t, rt, repo, absentReplayRecipe(state))
				match := `scope:"op:` + frontier + `"`
				if frontier == "pin" || frontier == "load" {
					match = ":" + frontier + `"`
				}
				fault := &gitOutcomeInterruption{WorkflowRuntime: rt, match: match, includeBase: true}
				_, err := runGitLifecycle(t, fault, key)
				require.Error(t, err)
				require.True(t, fault.fired)
				require.Equal(t, 1, publications)
				// Resume must retain the durable pin/load rather than substituting this
				// newly present branch-tip hook. Before pin, a fresh lookup may see it.
				tip := commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: present\nsequence: []\noutputs: {status: present}\n")
				if frontier == "publication" {
					// Keep the freshly discovered present body executable. The other cases
					// retain absence and must never reach the forbidden workspace.
					require.NoError(t, runGit(repo, "git", "reset", "--hard", initial))
				}
				out, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				first := gitLifecycleOutput(t, out)
				require.Equal(t, map[string]any{"status": "skipped"}, first["result"])
				require.Equal(t, 1, publications, "publication must execute only once")
				history, err := jobdb.GetJobRun(testBackground(), rt, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true})
				require.NoError(t, err)
				var restartScope string
				resolutions := 0
				for _, entry := range history.Execution {
					if entry.Record.Kind == "task" && strings.Contains(entry.Record.ID, `task:"hook:`) {
						resolutions++
					}
					if entry.Record.Kind == "scope" && strings.Contains(entry.Record.ID, `scope:"op:later"`) {
						restartScope = entry.Record.ID
					}
					if entry.Record.Input != nil {
						require.NotContains(t, string(entry.Record.Input.Data), `"cell":"forbidden"`)
					}
				}
				require.Equal(t, 2, resolutions)
				require.NotEmpty(t, restartScope)
				// A local retained-prefix restart must replay publication and the same
				// absence decision. The later workspace's counter must stay identical.
				restarted, err := rt.SubmitRestartJob(testBackground(), jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: key, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: restartScope}, Cell: "test"}})
				require.NoError(t, err)
				out, err = runGitLifecycle(t, rt, restarted.JobKey)
				require.NoError(t, err)
				second := gitLifecycleOutput(t, out)
				require.Equal(t, first, second)
				require.Equal(t, 1, publications)
				require.NoError(t, os.RemoveAll(repo))
				worker := NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(worker).BuildEngine()
				require.NoError(t, err)
				_, err = engine.ReplayJobRun(testBackground(), jobworkflow.ReplayRunRequest{JobKey: restarted.JobKey})
				require.NoError(t, err)
				require.Equal(t, 1, publications)
				require.NotEqual(t, initial, tip)
			})
		}
	}
}

// Fail the first synthetic-body evaluation inside the wrapper's retry scope.
// A source change at that moment must not cause another pin or load.
type retryAbsentBodyExecutor struct {
	DefaultRecipeExecutor
	t     *testing.T
	repo  string
	calls int
}

func (d *retryAbsentBodyExecutor) ExecuteSequence(ctx workflow.Context, parent *template.ResolutionContext, meta recipe.NodeMetadata, outputs map[string]any, nodes []recipe.Node) error {
	if meta.ID == "absent_hook" {
		d.calls++
		if d.calls == 1 {
			commitHookFile(d.t, d.repo, ".c2/hooks/check.yaml", "id: present\nsequence: []\noutputs: {status: present}\n")
			return fmt.Errorf("retry fallback evaluation")
		}
	}
	return d.DefaultRecipeExecutor.ExecuteSequence(ctx, parent, meta, outputs, nodes)
}
func TestHookAbsentRetryReusesDecision(t *testing.T) {
	for _, state := range []bool{false, true} {
		t.Run(fmt.Sprintf("state=%t", state), func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "A")
			ws := workspaceTestWorker(t)
			executor := &retryAbsentBodyExecutor{t: t, repo: repo}
			executor.DefaultRecipeExecutor = DefaultRecipeExecutor{}.WithDelegate(executor)
			ws.JobWorker = NewRecipeJobWorker(RecipeJobWorkerOptions{Executor: executor})
			var targetCalls atomic.Int32
			poisonAbsentWorkspace(ws, &targetCalls)
			ctx, eng, _, _ := hookTestEngine(t, ws)
			text := absentWorkspaceRecipe(state, "workspace: {cell: forbidden}", "status: skipped", "retry: {maximum_attempts: 2, initial_interval: 1ms}")
			key := startHookTest(t, ctx, eng, repo, text)
			out, err := swfutil.JobResult(ctx, eng, key)
			require.NoError(t, err)
			require.JSONEq(t, `{"result":{"status":"skipped"},"continued":true}`, string(out.GetDataOrPanic()))
			require.Equal(t, 2, executor.calls)
			require.Zero(t, targetCalls.Load())
			run, err := eng.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key})
			require.NoError(t, err)
			count := 0
			for _, task := range executionTaskRuns(run) {
				if task.TaskType == hookResolveTaskType {
					count++
				}
			}
			require.Equal(t, 2, count, "one durable pin and load across execution retries")
		})
	}
}

func TestHookAbsentRestartDiscardedLoadResolvesAgain(t *testing.T) {
	for _, state := range []bool{false, true} {
		t.Run(fmt.Sprintf("state=%t", state), func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "A")
			publications := 0
			publication := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "absent_publication"}, func(_ coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
				publications++
				return map[string]any{}, nil
			})
			workspaceTestWorker(t, publication)
			inner := absentWorkspaceRecipe(state, "workspace: {cell: test}", "status: skipped", "")
			// A containing include-free sequence is the supported restart boundary
			// before source discovery. Root declarations are ordinary node declarations.
			inner = strings.TrimPrefix(inner, "id: absent-workspace\n")
			text := "id: git-lifecycle\nsequence:\n - id: publication\n   op: absent_publication\n - id: refresh\n" + indentAbsent(inner, 3) + "\noutputs: {result: '${{ sequence.refresh.outputs.result }}'}\n"
			rt := newTimeoutReproRuntime(t, "sqlite")
			key := submitGitLifecycle(t, rt, repo, text)
			out, err := runGitLifecycle(t, rt, key)
			require.NoError(t, err)
			require.Equal(t, map[string]any{"status": "skipped"}, gitLifecycleOutput(t, out)["result"])
			run, err := jobdb.GetJobRun(testBackground(), rt, jobdb.GetJobRunRequest{JobKey: key})
			require.NoError(t, err)
			var scope string
			kind := "sequence"
			if state {
				kind = "state_machine"
			}
			for _, entry := range run.Execution {
				if entry.Record.Kind == "scope" && strings.HasSuffix(entry.Record.ID, `scope:"`+kind+`:refresh"#1`) {
					scope = entry.Record.ID
				}
			}
			require.NotEmpty(t, scope)
			commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: present\nsequence: []\noutputs: {status: present}\n")
			restart, err := rt.SubmitRestartJob(testBackground(), jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: key, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: scope}, Cell: "test"}})
			require.NoError(t, err)
			out, err = runGitLifecycle(t, rt, restart.JobKey)
			require.NoError(t, err)
			require.Equal(t, map[string]any{"status": "present"}, gitLifecycleOutput(t, out)["result"])
			require.Equal(t, 1, publications)
		})
	}
}
