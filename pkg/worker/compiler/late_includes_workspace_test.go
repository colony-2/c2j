package compiler

import (
	"context"
	"fmt"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestLateIncludeManagedWorkspacePreservation(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint("explicit=", explicit), func(t *testing.T) {
			ws := workspaceTestWorker(t)
			f := newLateGitFixture(t)
			bodyInput := "read: dirty.txt"
			if explicit {
				bodyInput = "write: selected.txt, absent: dirty.txt"
			}
			commit := commitHookFile(t, f.dir, "child.yaml", "id: selected-source\nsequence:\n- id: probe\n  op: workspace_probe\n  inputs: {"+bodyInput+"}\noutputs: {cell: '${{ sequence.probe.outputs.cell }}', owner: '${{ sequence.probe.outputs.owner }}', scope: '${{ sequence.probe.outputs.scope }}', text: '${{ sequence.probe.outputs.text }}', head: '${{ sequence.probe.outputs.head }}'}\n")
			workspace := ""
			if explicit {
				workspace = fmt.Sprintf("  workspace: {cell: '${{ sequence.before.outputs.cell }}', repo: %q, ref: %q}\n", f.url, f.initial)
			}
			rec := f.expand(t, fmt.Sprintf(`id: root
workspace: {cell: test, repo: %q, ref: %q}
sequence:
- id: before
  op: workspace_probe
  inputs: {write: dirty.txt}
- id: call
  include: {bind: late, recipe: %q}
%s- id: after
  op: workspace_probe
  inputs: {read: dirty.txt, absent: selected.txt}
outputs:
 before: '${{ sequence.before.outputs }}'
 body: '${{ sequence.call.outputs }}'
 after: '${{ sequence.after.outputs }}'
`, f.url, f.initial, f.selector("child.yaml", commit), workspace))
			rt := newTimeoutReproRuntime(t, "sqlite")
			guard := lateGuard(rt)
			w := lateRealWorker(t, rec, f)
			celltest.Create(t, rt, "scope-tests", "test", "late.test/"+strings.TrimPrefix(f.url, "https://late.test/"))
			submitted, err := rt.SubmitJob(lateTestContext(), jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "scope-tests", JobType: w.Name(), Data: jobdb.NewTaskDataOrPanic(nil), Cell: "test", CellRepo: strings.TrimPrefix(f.url, "https://")}})
			require.NoError(t, err)
			key := submitted.JobKey
			out := lateRun(t, guard, key, w, lateRealTasks(t, guard, ws)...)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
			values := lateJobOutput(t, rt, key)
			before := values["before"].(map[string]any)
			body := values["body"].(map[string]any)
			after := values["after"].(map[string]any)
			require.Equal(t, "test data", after["text"])
			require.Equal(t, before["scope"], after["scope"])
			require.Equal(t, "test", body["cell"])
			require.Equal(t, "test", body["owner"])
			require.NotEqual(t, commit, body["head"], "selected source commit must never pin the managed workspace")
			if explicit {
				require.NotEqual(t, before["scope"], body["scope"])
			} else {
				require.Equal(t, before["scope"], body["scope"])
				require.Equal(t, after["head"], body["head"])
				require.Equal(t, "test data", body["text"])
			}
			s, c, _ := lateRecordOutputs(t, rt, key)
			require.Len(t, s, 1)
			require.Len(t, c, 1)
			require.Len(t, s[0].Invocation, 64)
			f.available.Store(false)
			require.NoError(t, probeReplay(t, rt, key, w))
		})
	}
}

func TestLateIncludeCanIntroduceOrdinaryHooks(t *testing.T) {
	for _, mode := range []string{"legacy", "execution"} {
		t.Run(mode, func(t *testing.T) {
			ws := workspaceTestWorker(t)
			f := newLateGitFixture(t)
			f.initial = commitHookFile(t, f.dir, ".c2/hooks/check.yaml", "id: check\nsequence: []\noutputs: {value: hook}\n")
			commit := commitHookFile(t, f.dir, "selected.yaml", "id: selected\nsequence:\n- id: hook\n  include: cell://.c2/hooks/check.yaml\noutputs: {value: '${{ sequence.hook.outputs.value }}'}\n")
			rec := f.expand(t, fmt.Sprintf("id: root\nsource_hooks: from_current\nsequence:\n- id: call\n  include: {bind: late, recipe: %q}\noutputs: {value: '${{ sequence.call.outputs.value }}'}\n", f.selector("selected.yaml", commit)))
			rt := newTimeoutReproRuntime(t, "sqlite")
			guard := lateGuard(rt)
			w := lateHookFormatWorker(t, rec, f, mode == "execution")
			key := probeSubmit(t, rt, w)
			out := lateRun(t, guard, key, w, lateRealTasks(t, guard, ws)...)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
			require.Equal(t, "hook", lateJobOutput(t, rt, key)["value"])
			history, err := jobdb.GetJobRun(lateTestContext(), rt, jobdb.GetJobRunRequest{JobKey: key})
			require.NoError(t, err)
			var policyTask bool
			for _, task := range executionTaskRuns(history) {
				policyTask = policyTask || task.TaskType == hookPolicyTaskType
			}
			require.Equal(t, mode == "legacy", policyTask)
			f.available.Store(false)
			require.NoError(t, probeReplay(t, rt, key, w))
		})
	}
}

func TestLateIncludeKeepsHookRepositoryAcrossFormats(t *testing.T) {
	for _, fixture := range []struct {
		name, want string
		rootHook   bool
	}{
		{name: "first-hooks-use-caller-workspace", want: "foreign"},
		{name: "existing-binding-keeps-root", want: "root", rootHook: true},
	} {
		for _, mode := range []string{"legacy", "execution"} {
			t.Run(fixture.name+"/"+mode, func(t *testing.T) {
				workers := workspaceTestWorker(t)
				root := newLateGitFixture(t)
				foreign := newLateGitFixture(t)
				const verifyHook = ".c2/recipes/hooks/build/verify.yaml"
				root.initial = commitHookFile(t, root.dir, verifyHook, "id: verify\nsequence: []\noutputs: {repository: root}\n")
				foreign.initial = commitHookFile(t, foreign.dir, verifyHook, "id: verify\nsequence: []\noutputs: {repository: foreign}\n")
				selected := commitHookFile(t, root.dir, "selected.yaml", fmt.Sprintf(`id: selected
sequence:
- id: verify
  include: cell://%s
outputs: {repository: '${{ sequence.verify.outputs.repository }}'}
`, verifyHook))
				rootBinding := ""
				if fixture.rootHook {
					rootBinding = fmt.Sprintf("- id: root-verify\n  when: 'false'\n  include: cell://%s\n", verifyHook)
				}
				rec := root.expand(t, fmt.Sprintf(`id: root
source_hooks: from_current
sequence:
%s- id: foreign
  workspace: {cell: foreign, repo: %q, ref: %q}
  sequence:
  - id: call
    include: {bind: late, recipe: %q}
  outputs: {repository: '${{ sequence.call.outputs.repository }}'}
outputs: {repository: '${{ sequence.foreign.outputs.repository }}'}
`, rootBinding, foreign.url, foreign.initial, root.selector("selected.yaml", selected)))
				runtime := newTimeoutReproRuntime(t, "sqlite")
				celltest.Create(t, runtime, "scope-tests", "foreign", celltest.Repo(foreign.url))
				guard := lateGuard(runtime)
				worker := lateHookFormatWorker(t, rec, root, mode == "execution")
				key := probeSubmit(t, runtime, worker)
				outcome := lateRun(t, guard, key, worker, lateRealTasks(t, guard, workers)...)
				require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
				require.Equal(t, fixture.want, lateJobOutput(t, runtime, key)["repository"])
				root.available.Store(false)
				foreign.available.Store(false)
				require.NoError(t, probeReplay(t, runtime, key, worker))
			})
		}
	}
}

func TestLateIncludeExplicitWorkspaceRetryMatchesEager(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint("late=", late), func(t *testing.T) {
			var attempts atomic.Int32
			probe := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "late_workspace_retry"}, func(deps coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
				if _, err := os.Stat(filepath.Join(deps.WorktreePath(), "root-dirty.txt")); !os.IsNotExist(err) {
					return nil, fmt.Errorf("explicit workspace inherited dirty caller bytes")
				}
				if attempts.Add(1) == 1 {
					return nil, fmt.Errorf("temporary failure")
				}
				g := deps.GitContext()
				return map[string]any{"cell": g.Workspace.Cell, "scope": g.Workspace.ScopeID, "owner": g.CellName}, nil
			})
			ws := workspaceTestWorker(t, probe)
			f := newLateGitFixture(t)
			commit := commitHookFile(t, f.dir, "child.yaml", "id: selected\nsequence:\n- id: effect\n  op: late_workspace_retry\noutputs: {cell: '${{ sequence.effect.outputs.cell }}', scope: '${{ sequence.effect.outputs.scope }}', owner: '${{ sequence.effect.outputs.owner }}'}\n")
			include := fmt.Sprintf("include: %q", f.selector("child.yaml", commit))
			if late {
				include = fmt.Sprintf("include: {bind: late, recipe: %q}", f.selector("child.yaml", commit))
			}
			rec := f.expand(t, fmt.Sprintf("id: root\nsequence:\n- id: before\n  op: workspace_probe\n  inputs: {write: root-dirty.txt}\n- id: call\n  %s\n  workspace: {cell: '${{ sequence.before.outputs.cell }}', repo: %q, ref: %q}\n  retry: {maximum_attempts: 2, initial_interval: 1ms}\n- id: after\n  op: workspace_probe\n  inputs: {read: root-dirty.txt}\noutputs: {body: '${{ sequence.call.outputs }}', caller: '${{ sequence.after.outputs.text }}'}\n", include, f.url, f.initial))
			rt := newTimeoutReproRuntime(t, "sqlite")
			guard := lateGuard(rt)
			w := lateRealWorker(t, rec, f)
			celltest.Create(t, rt, "scope-tests", "test", strings.TrimPrefix(f.url, "https://"))
			submitted, err := rt.SubmitJob(lateTestContext(), jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "scope-tests", JobType: w.Name(), Data: jobdb.NewTaskDataOrPanic(nil), Cell: "test", CellRepo: strings.TrimPrefix(f.url, "https://")}})
			require.NoError(t, err)
			key := submitted.JobKey
			out := lateRun(t, guard, key, w, lateRealTasks(t, guard, ws)...)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
			require.Equal(t, int32(2), attempts.Load())
			values := lateJobOutput(t, rt, key)
			require.Equal(t, "test data", values["caller"])
			body := values["body"].(map[string]any)
			require.Equal(t, "test", body["cell"])
			require.Equal(t, "test", body["owner"])
			require.NotEmpty(t, body["scope"])
			s, c, _ := lateRecordOutputs(t, rt, key)
			count := 0
			if late {
				count = 1
			}
			require.Len(t, s, count)
			require.Len(t, c, count)
			f.available.Store(false)
			require.NoError(t, probeReplay(t, rt, key, w))
			require.Equal(t, int32(2), attempts.Load())
		})
	}
}
