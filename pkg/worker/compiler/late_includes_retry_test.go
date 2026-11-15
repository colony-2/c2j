package compiler

import (
	"fmt"
	"testing"

	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestLateIncludeBodyRetryAndEnclosingRetry(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, state := range []bool{false, true} {
		for _, enclosing := range []bool{false, true} {
			t.Run(fmt.Sprintf("state=%t/enclosing=%t", state, enclosing), func(t *testing.T) {
				f := newLateGitFixture(t)
				commit := commitHookFile(t, f.dir, "child.yaml", "id: arbitrary-selected-id\nversion: '7'\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\noutputs: {ok: true}\n")
				body := fmt.Sprintf("id: root\nsequence:\n- id: call\n  include: {bind: late, recipe: %q}\n  retry: {maximum_attempts: 2, initial_interval: 1ms}\n", f.selector("child.yaml", commit))
				if state {
					body = fmt.Sprintf("id: root\nstate:\n initial: call\n states:\n  call:\n   include: {bind: late, recipe: %q}\n   retry: {maximum_attempts: 2, initial_interval: 1ms}\n", f.selector("child.yaml", commit))
				}
				if enclosing {
					body = fmt.Sprintf("id: root\nsequence:\n- id: enclosing\n  retry: {maximum_attempts: 2, initial_interval: 1ms}\n  sequence:\n  - id: call\n    include: {bind: late, recipe: %q}\n", f.selector("child.yaml", commit))
				}
				rec := f.expand(t, body)
				rt := newTimeoutReproRuntime(t, "sqlite")
				guard := lateGuard(rt)
				w := lateWorker(t, rec, nil)
				key := probeSubmit(t, rt, w)
				_, git := GenerateTestContext()
				probe := &scopeProbeTask{calls: map[string]int{}, failOnce: "body", git: git}
				out := lateRun(t, guard, key, w, lateTasks(t, guard, probe)...)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				require.Equal(t, 2, probe.calls["body"])
				s, c, _ := lateRecordOutputs(t, rt, key)
				count := 1
				if enclosing {
					count = 2
				}
				require.Len(t, s, count)
				require.Len(t, c, count)
				if enclosing {
					require.NotEqual(t, s[0].Invocation, s[1].Invocation)
				}
				f.available.Store(false)
				require.NoError(t, probeReplay(t, rt, key, w))
				require.Equal(t, 2, probe.calls["body"])
			})
		}
	}
}
