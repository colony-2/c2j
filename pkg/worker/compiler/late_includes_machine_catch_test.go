package compiler

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Remove the actual Git source only after the real body task has failed.
// Selection and capture workers are untouched.
type lateSourceLossWorker struct {
	jobworkflow.TaskWorker
	afterFailure func()
}

func (w lateSourceLossWorker) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	out, err := w.TaskWorker.Run(ctx, input)
	if err != nil {
		w.afterFailure()
	}
	return out, err
}

func TestLateIncludeStateRetryWithMachineCatch(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newLateGitFixture(t)
			commit := commitHookFile(t, f.dir, "child.yaml", "id: child\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\noutputs: {value: captured}\n")
			call := fmt.Sprintf("include: {bind: late, recipe: %q}\nretry: {maximum_attempts: 2, initial_interval: 1ms}\n", f.selector("child.yaml", commit))
			rec := f.expand(t, "catch:\n- when: false\n  continue: {outputs: {}}\n"+latePolicyRecipe(true, call))
			rt := newTimeoutReproRuntime(t, backend)
			guard := lateGuard(rt)
			w := lateWorker(t, rec, nil)
			key := probeSubmit(t, rt, w)
			_, git := GenerateTestContext()
			probe := &scopeProbeTask{calls: map[string]int{}, failOnce: "body", git: git}
			var reads int32
			body := lateSourceLossWorker{TaskWorker: probe, afterFailure: func() {
				f.available.Store(false)
				require.NoError(t, os.RemoveAll(f.dir))
				reads = f.reads.Load()
			}}
			out := lateRun(t, guard, key, w, lateTasks(t, guard, body)...)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
			require.Equal(t, 2, probe.calls["body"])
			require.Equal(t, 1, probe.calls["continued"])
			require.Equal(t, "captured", lateJobOutput(t, rt, key)["value"])
			s, c, run := lateRecordOutputs(t, rt, key)
			t.Logf("body attempts=%d selections=%d captures=%d", probe.calls["body"], len(s), len(c))
			require.Len(t, s, 1)
			require.Len(t, c, 1)
			artifacts := lateArtifactBytes(t, rt, key, run)
			require.Len(t, artifacts, 1)
			require.Equal(t, c[0].ContentSHA256, sha256Hex(artifacts[0]))
			require.Equal(t, reads, f.reads.Load(), "body retry must not access Git")
			require.NoError(t, probeReplay(t, rt, key, w))
			s2, c2, run2 := lateRecordOutputs(t, rt, key)
			require.Equal(t, s, s2)
			require.Equal(t, c, c2)
			require.Equal(t, run.Execution, run2.Execution)
			require.Equal(t, reads, f.reads.Load())
			require.Equal(t, 2, probe.calls["body"])
		})
	}
}

func TestLateIncludeStateDeadlineWithMachineCatch(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newLateGitFixture(t)
			commit := commitHookFile(t, f.dir, "child.yaml", "id: child\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\noutputs: {ok: true}\n")
			// Reach the body reliably before expiring during the longer retry wait.
			rec := f.expand(t, fmt.Sprintf("id: root\ncatch:\n- when: false\n  continue: {outputs: {}}\nstate:\n initial: call\n states:\n  call:\n   include: {bind: late, recipe: %q}\n   timeout: 2s\n   retry: {maximum_attempts: 2, initial_interval: 4s}\n", f.selector("child.yaml", commit)))
			rt := newTimeoutReproRuntime(t, backend)
			guard := lateGuard(rt)
			w := lateWorker(t, rec, nil)
			key := probeSubmit(t, rt, w)
			_, git := GenerateTestContext()
			probe := &scopeProbeTask{calls: map[string]int{}, failOnce: "body", git: git}
			started := time.Now()
			out := lateRun(t, guard, key, w, lateTasks(t, guard, probe)...)
			t.Logf("status=%s elapsed=%s body attempts=%d", out.Status, time.Since(started), probe.calls["body"])
			require.Equal(t, jobworkflow.JobRunFailed, out.Status, "total timeout must cover retry delay")
			require.ErrorContains(t, out.JobError, "timed out")
			require.Equal(t, 1, probe.calls["body"])
			s, c, _ := lateRecordOutputs(t, rt, key)
			require.Len(t, s, 1)
			require.Len(t, c, 1)
			f.available.Store(false)
			require.Error(t, probeReplay(t, rt, key, w))
			require.Equal(t, 1, probe.calls["body"])
		})
	}
}

func TestLateIncludeStateMachineCatchPrecedence(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, phase := range []string{"condition", "selection", "load", "capture", "schema", "body", "output"} {
		for _, handler := range []string{"machine-continue", "machine-route", "machine-fail", "state-continue"} {
			t.Run(phase+"/"+handler, func(t *testing.T) {
				f := newLateGitFixture(t)
				child := "id: child\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\noutputs: {value: good}\n"
				if phase == "capture" {
					child = "id: [malformed\n"
				}
				if phase == "schema" {
					child = "input_schema:\n missing: {type: string, required: true}\n" + child
				}
				commit := commitHookFile(t, f.dir, "child.yaml", child)
				selector := f.selector("child.yaml", commit)
				if phase == "selection" {
					selector = "./local.yaml"
				}
				if phase == "load" {
					selector = f.selector("missing.yaml", commit)
				}
				extra := ""
				if phase == "condition" {
					extra = "when: '1 / 0 == 1'\n"
				}
				if phase == "output" {
					extra = "expect_outputs: {type: object, properties: {value: {type: number}}}\n"
				}
				machine := "continue: {outputs: {value: 42}}"
				if handler == "machine-route" {
					machine = "to: recovered"
				}
				if handler == "machine-fail" {
					machine = "fail: {kind: task_error, code: REWRITTEN, message: machine-failure}"
				}
				if handler == "state-continue" {
					// The state handler wins and its fallback obeys the contract.
					extra += "catch:\n- when: true\n  continue: {outputs: {value: 42}}\n"
				}
				attempts := 3
				if handler == "machine-fail" {
					attempts = 1
				}
				call := fmt.Sprintf("include: {bind: late, recipe: %q}\nretry: {maximum_attempts: %d, initial_interval: 1ms}\n%s", selector, attempts, extra)
				rec := f.expand(t, "id: root\ncatch:\n- when: true\n  "+machine+"\nstate:\n initial: call\n states:\n  call:\n"+indentLate(call, "   ")+"   transitions: [{to: recovered}]\n  recovered:\n   op: scope_probe\n   inputs: {name: recovered}\noutputs: {value: recovered}\n")
				rt := newTimeoutReproRuntime(t, "sqlite")
				guard := lateGuard(rt)
				w := lateWorker(t, rec, nil)
				key := probeSubmit(t, rt, w)
				_, git := GenerateTestContext()
				probe := &scopeProbeTask{calls: map[string]int{}, git: git}
				if phase == "body" {
					probe.failOnce = "body"
				}
				out := lateRun(t, guard, key, w, lateTasks(t, guard, probe)...)
				if handler == "machine-fail" {
					require.Equal(t, jobworkflow.JobRunFailed, out.Status)
					require.ErrorContains(t, out.JobError, "machine-failure")
				} else {
					require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
					var want any = float64(42)
					if handler != "machine-continue" {
						want = "recovered"
					}
					require.Equal(t, want, lateJobOutput(t, rt, key)["value"])
				}
				wantBody := 0
				if phase == "body" || phase == "output" {
					wantBody = 1
				}
				require.Equal(t, wantBody, probe.calls["body"], "handlers must run before body retry")
				s, c, run := lateRecordOutputs(t, rt, key)
				wantSelection, wantCapture := 1, 1
				if phase == "condition" || phase == "selection" {
					wantSelection = 0
				}
				if phase == "condition" || phase == "selection" || phase == "load" || phase == "capture" {
					wantCapture = 0
				}
				require.Len(t, s, wantSelection)
				require.Len(t, c, wantCapture)
				matches := 0
				for _, entry := range run.Execution {
					if entry.Record.Kind == "catch" && entry.Record.Rule != "" {
						matches++
					}
				}
				require.Equal(t, 1, matches, "one authored handler per failure")
				f.available.Store(false)
				if handler == "machine-fail" {
					require.Error(t, probeReplay(t, rt, key, w))
				} else {
					require.NoError(t, probeReplay(t, rt, key, w))
				}
			})
		}
	}
}
