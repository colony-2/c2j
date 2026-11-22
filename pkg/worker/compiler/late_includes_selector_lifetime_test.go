package compiler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/shellcmd"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestLateIncludeSelectorProcessLifetime(t *testing.T) {
	testLateIncludeFunctionLifetime(t, "selector")
}

func TestLateIncludeInputProcessLifetime(t *testing.T) {
	testLateIncludeFunctionLifetime(t, "input")
}

func TestLateIncludeVariableProcessLifetime(t *testing.T) {
	testLateIncludeFunctionLifetime(t, "vars")
}

func TestLateIncludeOutputProcessLifetime(t *testing.T) {
	testLateIncludeFunctionLifetime(t, "outputs")
}

// Exercise real imported function processes in preparation and body evaluation, including
// their child shell/sleep. No selection/capture worker result is substituted.
func testLateIncludeFunctionLifetime(t *testing.T, phase string) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"sqlite", "remote"} {
		for _, layout := range []string{"sequence", "state", "state-machine-catch"} {
			for _, stop := range []string{"deadline", "cancellation"} {
				t.Run(backend+"/"+layout+"/"+stop, func(t *testing.T) {
					f := newLateGitFixture(t)
					startedMarker := filepath.Join(t.TempDir(), phase+"-started")
					lateMarker := filepath.Join(t.TempDir(), phase+"-continued")
					effectDelay := 2 * time.Second
					if stop == "deadline" {
						// The scope includes Git selection, capture and function imports.
						// Give that setup room on loaded hosts, while keeping the real
						// process's effect beyond the scope's entire timeout budget.
						effectDelay = 4 * time.Second
					}
					writeFunctionPackage(t, filepath.Join(f.dir, "tools", "functions"), "name: selectors\ntimeout: 10s\nfunctions:\n- name: slow_selector\n  mode: function\n  execution: sh selector.sh\n  args: [{name: value, schema: {type: string}}]\n  returns: {schema: {type: string}}\n", map[string]string{
						"selector.sh": fmt.Sprintf("printf started > %s\nsleep %.0f\nprintf forbidden > %s\ncat\n", shellcmd.QuotePOSIX(startedMarker), effectDelay.Seconds(), shellcmd.QuotePOSIX(lateMarker)),
					})
					child := "id: child\ninput_schema:\n value: {type: string, default_value: fallback}\nsequence:\n- id: body\n  op: scope_probe\n  inputs: {name: body}\noutputs: {value: good}\n"
					if phase == "vars" || phase == "outputs" {
						// Only the selected source imports this provider. Selection and
						// capture must finish before its variables/outputs can run.
						child = "id: child\nextensions:\n functions: [{selector: ./tools/functions, include: [slow_selector]}]\nsequence: []\n"
						if phase == "vars" {
							child += "vars: {value: '${{ slow_selector(\"callee-body\") }}'}\noutputs: {value: '${{ vars.value }}'}\n"
						} else {
							child += "outputs: {value: '${{ slow_selector(\"callee-output\") }}'}\n"
						}
					}
					commit := commitHookFile(t, f.dir, "child.yaml", child)
					selector := f.selector("child.yaml", commit)
					inputBinding := ""
					if phase == "selector" {
						selector = fmt.Sprintf("${{ slow_selector('%s') }}", selector)
					} else if phase == "input" {
						inputBinding = "inputs: {value: '${{ slow_selector(\"caller-input\") }}'}\n"
					}
					call := fmt.Sprintf("include: {bind: late, recipe: %q}\ncatch:\n- when: true\n  continue: {outputs: {value: forbidden-success}}\n", selector)
					call = inputBinding + call
					if stop == "deadline" {
						call = "timeout: 3s\n" + call
					}
					body := latePolicyRecipe(layout != "sequence", call)
					if layout == "state-machine-catch" {
						body = "catch:\n- when: true\n  continue: {outputs: {value: forbidden-success}}\n" + body
					}
					if phase == "selector" || phase == "input" {
						body = fmt.Sprintf("extensions:\n functions: [{selector: %q, include: [slow_selector]}]\n", f.selector("tools/functions", commit)) + body
					}
					rec := f.expand(t, body)
					rt := newTimeoutReproRuntime(t, backend)
					guard := lateGuard(rt)
					w := lateWorker(t, rec, nil)
					key := probeSubmit(t, rt, w)
					_, git := GenerateTestContext()
					probe := &scopeProbeTask{calls: map[string]int{}, git: git}
					ctx, cancel := context.WithCancel(lateTestContext())
					defer cancel()
					stopped := make(chan error, 1)
					if stop == "cancellation" {
						go func() {
							ticker := time.NewTicker(time.Millisecond)
							defer ticker.Stop()
							limit := time.NewTimer(10 * time.Second)
							defer limit.Stop()
							for {
								if _, err := os.Stat(startedMarker); err == nil {
									// Cancel only after the actual function process starts.
									err = rt.CancelJob(lateTestContext(), jobdb.CancelJobRequest{JobKey: key, Reason: "function cancellation"})
									cancel()
									stopped <- err
									return
								}
								select {
								case <-ticker.C:
								case <-limit.C:
									cancel()
									stopped <- fmt.Errorf("function never started")
									return
								case <-ctx.Done():
									stopped <- ctx.Err()
									return
								}
							}
						}()
					}
					h, err := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: w, TaskWorkers: lateTasks(t, guard, probe), WorkerID: "selector-lifetime", LeaseDuration: time.Minute})
					require.NoError(t, err)
					started := time.Now()
					out, runErr := h.Run(nil)
					elapsed := time.Since(started)
					if stop == "cancellation" {
						require.NoError(t, <-stopped)
						job, err := rt.GetJob(lateTestContext(), key)
						require.NoError(t, err)
						require.Equal(t, "function cancellation", job.CompletionDetail)
					} else {
						require.NoError(t, runErr)
						require.Equal(t, jobworkflow.JobRunFailed, out.Status)
						require.ErrorContains(t, out.JobError, "timed out")
					}
					require.NotEqual(t, jobworkflow.JobRunCompleted, out.Status, "run=%v", runErr)
					info, err := os.Stat(startedMarker)
					require.NoError(t, err, "exercise a running %s function", phase)
					// Wait beyond the script's write so leaked descendants are detected
					// even if execution returned promptly when its scope expired.
					time.Sleep(time.Until(info.ModTime().Add(effectDelay + 100*time.Millisecond)))
					require.NoFileExists(t, lateMarker, "%s function survived its execution context", phase)
					require.Empty(t, probe.calls)
					s, c, run := lateRecordOutputs(t, rt, key)
					if phase == "selector" {
						require.Empty(t, s, "canceled builder cannot accept selection")
						require.Empty(t, c)
					} else {
						require.Len(t, s, 1, "callee evaluation follows accepted selection")
						require.Len(t, c, 1, "callee evaluation follows verified capture")
						require.Equal(t, s[0], c[0].Selection)
						stored := lateArtifactBytes(t, rt, key, run)
						require.Len(t, stored, 1)
						require.Equal(t, c[0].ContentSHA256, sha256Hex(stored[0]))
					}
					for _, entry := range run.Execution {
						if entry.Record.Output != nil {
							require.NotContains(t, string(entry.Record.Output.Data), "forbidden-success")
						}
					}
					t.Logf("phase=%s status=%s elapsed=%s function started; late effect absent; selections=%d captures=%d body effects=%d", phase, out.Status, elapsed, len(s), len(c), probe.calls["body"])
				})
			}
		}
	}
}

func TestLateIncludeSelectorFunctionContextIsolation(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	f := newLateGitFixture(t)
	marker := filepath.Join(t.TempDir(), "function-effects")
	writeFunctionPackage(t, filepath.Join(f.dir, "tools", "functions"), "name: selectors\nfunctions:\n- name: selector_value\n  mode: function\n  execution: sh selector.sh\n  args: [{name: value, schema: {type: string}}]\n  returns: {schema: {type: string}}\n", map[string]string{
		"selector.sh": "printf x >> " + shellcmd.QuotePOSIX(marker) + "\ncat\n",
	})
	commit := commitHookFile(t, f.dir, "child.yaml", "id: child\ninput_schema:\n value: {type: string, required: true}\ninputs: {value: '${{ inputs.value }}'}\nsequence:\n- id: body\n  op: scope_probe\n  inputs: {name: body}\noutputs: {value: '${{ inputs.value }}'}\n")
	selector := fmt.Sprintf("${{ selector_value('%s') }}", f.selector("child.yaml", commit))
	rec := f.expand(t, fmt.Sprintf("id: root\nextensions:\n functions: [{selector: %q, include: [selector_value]}]\nsequence:\n- id: first\n  timeout: 1s\n  include: {bind: late, recipe: %q}\n  inputs: {value: '${{ selector_value(\"bound-input\") }}'}\n- id: second\n  timeout: 1s\n  include: {bind: late, recipe: %q}\n  inputs: {value: '${{ selector_value(\"bound-input\") }}'}\noutputs: {value: '${{ selector_value(\"caller\") }}', first: '${{ sequence.first.outputs.value }}', second: '${{ sequence.second.outputs.value }}'}\n", f.selector("tools/functions", commit), selector, selector))
	rt := newTimeoutReproRuntime(t, "sqlite")
	guard := lateGuard(rt)
	w := lateWorker(t, rec, nil)
	key := probeSubmit(t, rt, w)
	_, git := GenerateTestContext()
	probe := &scopeProbeTask{calls: map[string]int{}, git: git}
	out := lateRun(t, guard, key, w, lateTasks(t, guard, probe)...)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
	require.Equal(t, map[string]any{"value": "caller", "first": "bound-input", "second": "bound-input"}, lateJobOutput(t, rt, key))
	effects, err := os.ReadFile(marker)
	require.NoError(t, err)
	// Rebinding the lifetime preserves memoization, and leaving the include
	// does not leave the caller's function provider bound to an expired scope.
	require.Equal(t, "xxx", string(effects))
	s, c, _ := lateRecordOutputs(t, rt, key)
	require.Len(t, s, 2)
	require.Len(t, c, 2)
	require.NotEqual(t, s[0].Invocation, s[1].Invocation)
	require.Equal(t, 2, probe.calls["body"])
}

// A failed native scope closes its context. The next attempt and the catch's
// matcher/handler must use fresh lifetimes, without contaminating the caller.
func TestLateIncludeBodyFunctionContextIsolation(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"sqlite", "remote"} {
		for _, mode := range []string{"retry", "catch"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				f := newLateGitFixture(t)
				marker := filepath.Join(t.TempDir(), "effects")
				failed := filepath.Join(t.TempDir(), "failed")
				writeFunctionPackage(t, filepath.Join(f.dir, "tools", "functions"), "name: values\nfunctions:\n- name: selector_value\n  mode: function\n  execution: sh value.sh\n  args: [{name: value, schema: {type: string}}]\n  returns: {schema: {type: string}}\n", map[string]string{
					"value.sh": "request=$(cat)\ncase \"$request\" in *retry-output*) if ! test -e " + shellcmd.QuotePOSIX(failed) + "; then touch " + shellcmd.QuotePOSIX(failed) + "; exit 1; fi;; esac\nprintf x >> " + shellcmd.QuotePOSIX(marker) + "\nprintf '%s\\n' \"$request\"\n",
				})
				child := "id: child\nextensions:\n functions: [{selector: ./tools/functions, include: [selector_value]}]\nvars: {value: '${{ selector_value(\"vars\") }}'}\nsequence:\n- id: body\n  op: scope_probe\n  inputs: {name: body}\noutputs: {value: '${{ selector_value(\"retry-output\") }}'}\n"
				policy := "retry: {maximum_attempts: 2, initial_interval: 1ms}\n"
				if mode == "catch" {
					child = "id: child\nextensions:\n functions: [{selector: ./tools/functions, include: [selector_value]}]\nvars: {value: '${{ selector_value(\"vars\") }}'}\ncatch:\n- when: 'selector_value(\"match\") == \"match\"'\n  continue: {outputs: {value: '${{ selector_value(\"handled\") }}'}}\nsequence:\n- id: expired\n  timeout: 1s\n  sequence:\n  - id: body\n    op: scope_probe\n    inputs: {name: body}\noutputs: {value: unused}\n"
					policy = ""
				}
				commit := commitHookFile(t, f.dir, "child.yaml", child)
				rec := f.expand(t, fmt.Sprintf("id: root\nextensions:\n functions: [{selector: %q, include: [selector_value]}]\nsequence:\n- id: call\n  include: {bind: late, recipe: %q}\n%soutputs: {value: '${{ sequence.call.outputs.value }}', caller: '${{ selector_value(\"caller\") }}'}\n", f.selector("tools/functions", commit), f.selector("child.yaml", commit), indentLate(policy, "  ")))
				rt := newTimeoutReproRuntime(t, backend)
				guard := lateGuard(rt)
				w := lateWorker(t, rec, nil)
				key := probeSubmit(t, rt, w)
				_, git := GenerateTestContext()
				probe := &scopeProbeTask{calls: map[string]int{}, git: git}
				var task jobworkflow.TaskWorker = probe
				done := make(chan struct{})
				if mode == "catch" {
					task = scopeCancelTask{done}
				}
				out := lateRun(t, guard, key, w, lateTasks(t, guard, task)...)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				want, effects := "retry-output", "xxx"
				if mode == "catch" {
					want, effects = "handled", "xxxx"
					require.Eventually(t, func() bool {
						select {
						case <-done:
							return true
						default:
							return false
						}
					}, time.Second, time.Millisecond, "the real child task must receive cancellation")
				} else {
					require.Equal(t, 2, probe.calls["body"])
					require.FileExists(t, failed, "the first output evaluation actually failed")
				}
				require.Equal(t, map[string]any{"value": want, "caller": "caller"}, lateJobOutput(t, rt, key))
				actual, err := os.ReadFile(marker)
				require.NoError(t, err)
				require.Equal(t, effects, string(actual), "successful function memoization survives context rebinding")
				s, c, run := lateRecordOutputs(t, rt, key)
				require.Len(t, s, 1)
				require.Len(t, c, 1)
				require.Equal(t, s[0], c[0].Selection)
				stored := lateArtifactBytes(t, rt, key, run)
				require.Len(t, stored, 1)
				require.Equal(t, c[0].ContentSHA256, sha256Hex(stored[0]))
				t.Logf("%s: selections=1 captures=1 output=%s caller=caller effects=%d", mode, want, len(actual))
			})
		}
	}
}
