package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Emit the workspace-entry descriptor used by earlier compilers. All task
// identities, commands, results, and interrupted continuations are real JobDB
// history; only selection of the historical policy is controlled by the test.
type workspacePolicyWorker struct {
	jobworkflow.TaskWorker
	policy string
}

func (w workspacePolicyWorker) Run(ctx jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(ctx.Context(), ctx, data)
}

func (w workspacePolicyWorker) RunTask(local context.Context, ctx jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	out, err := coretask.RunLocal(w.TaskWorker, local, ctx, data)
	if err != nil {
		return nil, err
	}
	raw, err := out.GetData()
	if err != nil {
		return nil, err
	}
	var result WorkspaceResolutionOutput
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	result.Workspace.RefPolicy = w.policy
	return jobdb.NewTaskData(result)
}

func TestWorkspaceHistoryCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, policy, interrupt string
	}{
		{"entry-pinned", "", `scope:"op:first"`},
		{"legacy-workspace-base", contextual.WorkspaceFollowUntilChange, `task:"workspace-base"`},
		{"legacy-hook-base", contextual.WorkspaceFollowUntilChange, `task:"hook-base:`},
		{"legacy-hook-pin", contextual.WorkspaceFollowUntilChange, `:pin"`},
		{"per-task-hook-pin", contextual.WorkspaceFollowPerTask, `:pin"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "repo")
			workspaceTestWorker(t)
			commitHookFile(t, repo, ".c2/hooks/check.yaml", `id: check
sequence:
 - id: inspect
   op: workspace_probe
outputs: {head: '${{ sequence.inspect.outputs.head }}'}
`)
			yaml := fmt.Sprintf(`id: git-lifecycle
workspace: {cell: %q}
sequence:
 - id: first
   op: workspace_probe
 - id: hook
   include: cell://.c2/hooks/check.yaml
 - id: second_hook
   include: cell://.c2/hooks/check.yaml
 - id: last
   op: workspace_probe
outputs: {head: '${{ sequence.last.outputs.head }}'}
`, repo)
			rt := newTimeoutReproRuntime(t, "sqlite")
			key := submitGitLifecycle(t, rt, repo, yaml)
			fault := &gitOutcomeInterruption{WorkflowRuntime: rt, match: tc.interrupt, includeBase: true}
			interrupted, err := runGitLifecycle(t, fault, key, tc.policy)
			require.Error(t, err, "status=%s job error=%v", interrupted.Status, interrupted.JobError)
			require.True(t, fault.fired)
			require.NoError(t, runGit(repo, "git", "commit", "--allow-empty", "-m", "advance"))
			// Resume with ordinary, corrected workers. The recorded entry must
			// select its old execution path even at the unfinished frontier.
			out, err := runGitLifecycle(t, rt, key)
			require.NoError(t, err)
			gitLifecycleOutput(t, out)
			history, err := jobdb.GetJobRun(context.Background(), rt, jobdb.GetJobRunRequest{JobKey: key})
			require.NoError(t, err)
			var workspaceBases, hookBases int
			for _, entry := range history.Execution {
				if entry.Record.Kind != "result" {
					continue
				}
				if strings.Contains(entry.Record.ID, `task:"workspace-base"`) {
					workspaceBases++
				}
				if strings.Contains(entry.Record.ID, `task:"hook-base:`) {
					hookBases++
				}
			}
			if tc.policy == contextual.WorkspaceFollowUntilChange {
				require.Equal(t, 2, workspaceBases)
				require.Equal(t, 2, hookBases)
			} else {
				require.Zero(t, workspaceBases)
				require.Zero(t, hookBases)
			}
			require.NoError(t, os.Rename(repo, repo+"-offline"))
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{})).BuildEngine()
			require.NoError(t, err)
			replayed, err := engine.ReplayJobRun(context.Background(), jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
			raw, err := replayed.GetData()
			require.NoError(t, err)
			original, err := out.Output.GetData()
			require.NoError(t, err)
			require.JSONEq(t, string(original), string(raw))
		})
	}
}
