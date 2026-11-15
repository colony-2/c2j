package compiler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestLateIncludeRestartPrefixes(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"sqlite", "remote"} {
		for _, point := range []string{"retained-capture", "retained-selection", "discarded-selection"} {
			t.Run(backend+"/"+point, func(t *testing.T) {
				f := newLateGitFixture(t)
				first := commitHookFile(t, f.dir, "child.yaml", "id: old-selected-id\nversion: '1'\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: A}\noutputs: {value: A}\n")
				second := commitHookFile(t, f.dir, "child.yaml", "id: new-selected-id\nversion: '2'\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: B}\noutputs: {value: B}\n")
				rec := f.expand(t, "id: root\ninput_schema:\n target: {type: string, required: true}\ninputs: {target: '{{ inputs.target }}'}\nsequence:\n- id: call\n  include: {bind: late, recipe: '${{ inputs.target }}'}\noutputs: {value: '{{ sequence.call.outputs.value }}'}\n")
				live := f.selector("child.yaml", first)
				rt := newTimeoutReproRuntime(t, backend)
				guard := lateGuard(rt)
				w := lateWorker(t, rec, func() map[string]any { return map[string]any{"target": live} })
				key := probeSubmit(t, rt, w)
				_, git := GenerateTestContext()
				body := &scopeProbeTask{calls: map[string]int{}, git: git}
				out := lateRun(t, guard, key, w, lateTasks(t, guard, body)...)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				originalS, originalC, run := lateRecordOutputs(t, rt, key)
				require.Len(t, originalS, 1)
				require.Len(t, originalC, 1)
				var restartID string
				for _, e := range run.Execution {
					if e.Record.Kind != "task" {
						continue
					}
					descriptor := string(e.Record.Descriptor)
					if point == "retained-capture" && strings.Contains(descriptor, "scope_probe:run") {
						restartID = e.Record.ID
						break
					}
					if point == "retained-selection" && strings.Contains(descriptor, lateCaptureTaskType) {
						restartID = e.Record.ID
						break
					}
					if point == "discarded-selection" && strings.Contains(descriptor, lateSelectTaskType) {
						restartID = e.Record.ID
						break
					}
				}
				require.NotEmpty(t, restartID)
				next, err := rt.SubmitRestartJob(testBackground(), jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: key, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: restartID}, Cell: "test"}})
				require.NoError(t, err)
				require.NotEqual(t, key.JobId, next.JobKey.JobId)
				reads := f.reads.Load()
				live = "invalid-live-selection"
				if point == "retained-capture" {
					f.available.Store(false)
				}
				if point == "discarded-selection" {
					live = f.selector("child.yaml", second)
				}
				guard = lateGuard(rt)
				out = lateRun(t, guard, next.JobKey, w, lateTasks(t, guard, body)...)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				s, c, restarted := lateRecordOutputs(t, rt, next.JobKey)
				require.Len(t, s, 1)
				require.Len(t, c, 1)
				require.NotEqual(t, run.Job.GoalID, restarted.Job.GoalID)
				require.Equal(t, originalS[0].Invocation, s[0].Invocation, "same retained enclosing path/workspace/counter; target is not identity")
				if point == "discarded-selection" {
					require.Equal(t, second, s[0].Commit)
					require.Equal(t, 1, body.calls["B"])
				} else {
					require.Equal(t, originalS, s)
					require.Equal(t, 2, body.calls["A"])
				}
				if point == "retained-capture" {
					require.Equal(t, reads, f.reads.Load())
					require.Equal(t, originalC, c)
					for _, e := range restarted.Execution {
						if e.Record.Output != nil {
							for _, a := range e.Record.Output.Artifacts {
								if a.Name == ResolvedRecipeYAMLArtifactName {
									require.Equal(t, key.JobId, a.Key.JobId, "inherited artifact keys remain owned by the original backend record")
								}
							}
						}
					}
				} else {
					require.Greater(t, f.reads.Load(), reads)
				}
				f.available.Store(false)
				require.NoError(t, probeReplay(t, rt, next.JobKey, w))
			})
		}
	}
}
func TestLateIncludeStateRevisitsUseDistinctOccurrences(t *testing.T) {
	f := newLateGitFixture(t)
	a := commitHookFile(t, f.dir, "child.yaml", "id: first-id\nversion: '1'\nsequence: []\noutputs: {value: A}\n")
	b := commitHookFile(t, f.dir, "child.yaml", "id: different-id\nversion: '9'\nsequence: []\noutputs: {value: B}\n")
	selector := fmt.Sprintf("${{ has(states.call) ? '%s' : '%s' }}", f.selector("child.yaml", b), f.selector("child.yaml", a))
	rec := f.expand(t, fmt.Sprintf("id: root\nstate:\n initial: call\n states:\n  call:\n   include: {bind: late, recipe: %q}\n   transitions:\n   - {to: call, when: \"states.call.outputs.value == 'A'\"}\n   - {to: done}\n  done:\n   sequence: []\noutputs: {value: '{{ states.call.outputs.value }}'}\n", selector))
	rt := newTimeoutReproRuntime(t, "sqlite")
	guard := lateGuard(rt)
	w := lateWorker(t, rec, nil)
	key := probeSubmit(t, rt, w)
	out := lateRun(t, guard, key, w, lateTasks(t, guard)...)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
	s, c, _ := lateRecordOutputs(t, rt, key)
	require.Len(t, s, 2)
	require.Len(t, c, 2)
	require.NotEqual(t, s[0].Invocation, s[1].Invocation)
	require.Equal(t, a, s[0].Commit)
	require.Equal(t, b, s[1].Commit)
	require.JSONEq(t, `{"value":"B"}`, string(out.Output.GetDataOrPanic()))
	f.available.Store(false)
	require.NoError(t, probeReplay(t, rt, key, w))
}
