package compiler

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/git/selectorcache"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func latePolicyRecipe(state bool, call string) string {
	if state {
		return "id: root\nstate:\n initial: call\n states:\n  call:\n" + indentLate(call, "   ") + "   transitions: [{to: continuation}]\n  continuation:\n   op: scope_probe\n   inputs: {name: continued}\noutputs: {value: '${{ states.call.outputs.value }}'}\n"
	}
	return "id: root\nsequence:\n- id: call\n" + indentLate(call, "  ") + "- id: continuation\n  op: scope_probe\n  inputs: {name: continued}\noutputs: {value: '${{ sequence.call.outputs.value }}'}\n"
}
func TestLateIncludeFailureHandling(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, state := range []bool{false, true} {
		for _, phase := range []string{"selection", "load", "capture", "schema", "body", "output"} {
			t.Run(fmt.Sprintf("state=%t/%s", state, phase), func(t *testing.T) {
				f := newLateGitFixture(t)
				child := "id: selected\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\noutputs: {value: good}\n"
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
					selector = f.selector("absent.yaml", commit)
				}
				extra := ""
				if phase == "output" {
					extra = "expect_outputs: {type: object, properties: {value: {type: integer}}}\n"
				}
				call := fmt.Sprintf("include: {bind: late, recipe: %q}\nretry: {maximum_attempts: 3, initial_interval: 1ms}\n%scatch:\n- when: 'true'\n  continue: {outputs: {value: handled}}\n", selector, extra)
				rec := f.expand(t, latePolicyRecipe(state, call))
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
				// A continued catch still obeys its authored output contract.
				if phase == "output" {
					require.Equal(t, jobworkflow.JobRunFailed, out.Status)
					require.Zero(t, probe.calls["continued"])
				} else {
					require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
					require.Equal(t, 1, probe.calls["continued"])
				}
				s, c, _ := lateRecordOutputs(t, rt, key)
				if phase == "selection" {
					require.Empty(t, s)
				} else {
					require.Len(t, s, 1)
				}
				if phase == "load" || phase == "capture" || phase == "selection" {
					require.Empty(t, c)
					require.Empty(t, probe.calls["body"])
				} else {
					require.Len(t, c, 1)
				}
				if phase == "schema" {
					require.Zero(t, probe.calls["body"])
				}
				if phase == "body" {
					require.Equal(t, 1, probe.calls["body"], "ordinary catches run before retry")
				}
			})
		}
	}
}

type lateBlockingResolver struct {
	RecipeSourceResolver
	delay              time.Duration
	started, cancelled atomic.Bool
}

func (r *lateBlockingResolver) LoadYAML(ctx context.Context, project string, source RecipeSourceResolution) ([]byte, error) {
	r.started.Store(true)
	timer := time.NewTimer(r.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		r.cancelled.Store(true)
		return nil, context.Cause(ctx)
	case <-timer.C:
	}
	return r.RecipeSourceResolver.(RecipeSourceYAMLLoader).LoadYAML(ctx, project, source)
}

func TestLateIncludeDeadlineCoversPreparationAndBody(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, state := range []bool{false, true} {
		for _, phase := range []string{"preparation", "body", "retry-budget"} {
			t.Run(fmt.Sprintf("state=%t/%s", state, phase), func(t *testing.T) {
				f := newLateGitFixture(t)
				commit := commitHookFile(t, f.dir, "child.yaml", "id: child\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\noutputs: {value: good}\n")
				timeout, delay, interval := 2*time.Second, 250*time.Millisecond, 300*time.Millisecond
				if phase == "retry-budget" {
					// Each delay fits a fresh budget, but their sum does not. Leave
					// ample setup time and avoid racing a retry against scope expiry.
					delay, interval = 1200*time.Millisecond, 1200*time.Millisecond
				}
				call := fmt.Sprintf("include: {bind: late, recipe: %q}\ntimeout: %s\nretry: {maximum_attempts: 5, initial_interval: %s}\n", f.selector("child.yaml", commit), timeout, interval)
				rec := f.expand(t, latePolicyRecipe(state, call))
				rt := newTimeoutReproRuntime(t, "sqlite")
				guard := lateGuard(rt)
				w := lateWorker(t, rec, nil)
				key := probeSubmit(t, rt, w)
				resolver := &lateBlockingResolver{RecipeSourceResolver: NewRecipeSourceResolver(RecipeSourceResolverOptions{SelectorCache: &selectorcache.Cache{Root: t.TempDir()}}), delay: delay}
				if phase == "preparation" {
					resolver.delay = time.Minute
				}
				_, git := GenerateTestContext()
				probe := &scopeProbeTask{calls: map[string]int{}, git: git, failOnce: "body"}
				tasks := lateTasks(t, guard, probe)
				done := make(chan struct{})
				if phase == "body" {
					tasks = lateTasks(t, guard, scopeCancelTask{done})
				}
				for i, task := range tasks {
					if task.Name() == lateCaptureTaskType {
						tasks[i] = guard.WrapTaskWorker(lateCaptureWorker{resolver: resolver})
					}
				}
				started := time.Now()
				out := lateRun(t, guard, key, w, tasks...)
				require.Equal(t, jobworkflow.JobRunFailed, out.Status, "%v", out.JobError)
				require.ErrorContains(t, out.JobError, "timed out")
				require.Zero(t, probe.calls["continued"])
				require.Less(t, time.Since(started), timeout+time.Second)
				if phase == "preparation" {
					// The scope may return before the canceled task goroutine exits.
					require.Eventually(t, resolver.cancelled.Load, time.Second, time.Millisecond)
					require.Zero(t, f.reads.Load())
				}
				if phase == "body" {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("body did not receive cancellation")
					}
				}
				if phase == "retry-budget" {
					selections, captures, run := lateRecordOutputs(t, rt, key)
					require.Len(t, selections, 1)
					require.Len(t, captures, 1)
					var deadline time.Time
					for _, entry := range run.Execution {
						r := entry.Record
						if r.TotalDeadline.IsZero() {
							continue
						}
						if deadline.IsZero() {
							deadline = r.TotalDeadline
							require.Equal(t, timeout, deadline.Sub(r.At))
						}
						require.Equal(t, deadline, r.TotalDeadline, "preparation and body retain the same durable deadline")
						if !r.Deadline.IsZero() {
							require.Equal(t, deadline, r.Deadline)
						}
					}
					require.False(t, deadline.IsZero())
					if probe.calls["body"] != 1 {
						for _, entry := range run.Execution {
							r := entry.Record
							t.Logf("%s %s at=%s deadline=%s total=%s retry=%t wake=%s", r.Kind, r.ID, r.At, r.Deadline, r.TotalDeadline, r.Retry, r.WakeAt)
						}
					}
					require.Equal(t, 1, probe.calls["body"], "preparation and retry delay share one budget")
				}
			})
		}
	}
}

type lateCancelWorker struct {
	jobworkflow.TaskWorker
	cancel func()
}

func (w lateCancelWorker) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	w.cancel()
	return nil, context.Canceled
}

type lateCancelRuntime struct {
	jobdb.WorkflowRuntime
	cancel func()
	verify bool
}

func (r *lateCancelRuntime) OpenArtifact(ctx context.Context, ref jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	reader, err := r.WorkflowRuntime.OpenArtifact(ctx, ref)
	if err == nil && r.verify && ref.Name == ResolvedRecipeYAMLArtifactName {
		return lateCancelArtifactReader{reader, r.cancel}, nil
	}
	return reader, err
}

type lateCancelArtifactReader struct {
	jobdb.ArtifactReader
	cancel func()
}

func (r lateCancelArtifactReader) Open() (io.ReadCloser, error) {
	stream, err := r.ArtifactReader.Open()
	if err != nil {
		return nil, err
	}
	return &lateCancelArtifactStream{ReadCloser: stream, cancel: r.cancel}, nil
}

type lateCancelArtifactStream struct {
	io.ReadCloser
	cancel func()
}

func (r *lateCancelArtifactStream) Read(bytes []byte) (int, error) {
	n, err := r.ReadCloser.Read(bytes)
	if n > 0 {
		r.cancel()
		return n, context.Canceled
	}
	return n, err
}

type lateCancelResolver struct {
	RecipeSourceResolver
	cancel func()
}

func (r lateCancelResolver) LoadYAML(ctx context.Context, project string, source RecipeSourceResolution) ([]byte, error) {
	// Let the real loader fetch the target; cancel before its result can be captured.
	bytes, err := r.RecipeSourceResolver.(RecipeSourceYAMLLoader).LoadYAML(ctx, project, source)
	r.cancel()
	if err != nil {
		return nil, err
	}
	return bytes, context.Canceled
}
func TestLateIncludeCancellationNeverContinues(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, mode := range []struct{ state, machine bool }{{false, false}, {true, false}, {true, true}} {
		state, machine := mode.state, mode.machine
		for _, phase := range []string{"selection", "capture", "verification", "body"} {
			t.Run(fmt.Sprintf("state=%t/machine=%t/%s", state, machine, phase), func(t *testing.T) {
				f := newLateGitFixture(t)
				commit := commitHookFile(t, f.dir, "child.yaml", "id: child\nsequence:\n- id: body\n  op: scope_probe\n  inputs: {name: body}\noutputs: {value: good}\n")
				call := fmt.Sprintf("include: {bind: late, recipe: %q}\ncatch:\n- when: 'true'\n  continue: {outputs: {value: forbidden-success}}\n", f.selector("child.yaml", commit))
				body := latePolicyRecipe(state, call)
				if machine {
					body = "catch:\n- when: true\n  continue: {outputs: {value: forbidden-success}}\n" + body
				}
				rec := f.expand(t, body)
				rt := newTimeoutReproRuntime(t, "sqlite")
				w := lateWorker(t, rec, nil)
				key := probeSubmit(t, rt, w)
				if phase == "verification" {
					lost, cancelLost := context.WithCancel(lateTestContext())
					interruption := &lateInterruption{WorkflowRuntime: rt, phase: "capture", cancel: cancelLost}
					firstGuard := lateGuard(interruption)
					first, err := jobworkflow.GetJobForRun(lost, firstGuard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: w, TaskWorkers: lateTasks(t, firstGuard), WorkerID: "before-verification", LeaseDuration: time.Minute})
					require.NoError(t, err)
					_, err = first.Run(nil)
					cancelLost()
					require.Error(t, err)
					require.True(t, interruption.fired)
					f.available.Store(false)
				}
				ctx, cancel := context.WithCancel(lateTestContext())
				defer cancel()
				var fired atomic.Bool
				stop := func() {
					if fired.CompareAndSwap(false, true) {
						require.NoError(t, rt.CancelJob(lateTestContext(), jobdb.CancelJobRequest{JobKey: key, Reason: "late cancellation test"}))
						cancel()
					}
				}
				wrapped := &lateCancelRuntime{WorkflowRuntime: rt, cancel: stop, verify: phase == "verification"}
				guard := lateGuard(wrapped)
				_, git := GenerateTestContext()
				probe := &scopeProbeTask{calls: map[string]int{}, git: git}
				tasks := lateTasks(t, guard, probe)
				for i, task := range tasks {
					if (phase == "selection" && task.Name() == lateSelectTaskType) || (phase == "body" && task.Name() == probe.Name()) {
						tasks[i] = lateCancelWorker{TaskWorker: task, cancel: stop}
					}
					if phase == "capture" && task.Name() == lateCaptureTaskType {
						resolver := NewRecipeSourceResolver(RecipeSourceResolverOptions{SelectorCache: &selectorcache.Cache{Root: t.TempDir()}})
						tasks[i] = guard.WrapTaskWorker(lateCaptureWorker{resolver: lateCancelResolver{resolver, stop}})
					}
				}
				h, err := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: w, TaskWorkers: tasks, WorkerID: "cancel-test", LeaseDuration: time.Minute})
				require.NoError(t, err)
				out, runErr := h.Run(nil)
				require.True(t, fired.Load())
				require.NotEqual(t, jobworkflow.JobRunCompleted, out.Status, "run=%v", runErr)
				require.Zero(t, probe.calls["body"])
				require.Zero(t, probe.calls["continued"])
				job, err := rt.GetJob(lateTestContext(), key)
				require.NoError(t, err)
				require.Equal(t, "late cancellation test", job.CompletionDetail)
				_, _, run := lateRecordOutputs(t, rt, key)
				for _, entry := range run.Execution {
					if entry.Record.Output != nil {
						require.NotContains(t, string(entry.Record.Output.Data), "forbidden-success")
					}
				}
			})
		}
	}
}

func TestLateIncludeCatchRewriteRunsOnce(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, state := range []bool{false, true} {
		t.Run(fmt.Sprint("state=", state), func(t *testing.T) {
			f := newLateGitFixture(t)
			commit := commitHookFile(t, f.dir, "child.yaml", "id: child\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\noutputs: {value: good}\n")
			call := fmt.Sprintf("include: {bind: late, recipe: %q}\nretry: {maximum_attempts: 1, initial_interval: 1ms}\ncatch:\n- when: 'true'\n  fail: {kind: task_error, code: REWRITTEN, message: rewritten-once}\n", f.selector("child.yaml", commit))
			rec := f.expand(t, latePolicyRecipe(state, call))
			rt := newTimeoutReproRuntime(t, "sqlite")
			guard := lateGuard(rt)
			w := lateWorker(t, rec, nil)
			key := probeSubmit(t, rt, w)
			_, git := GenerateTestContext()
			probe := &scopeProbeTask{calls: map[string]int{}, git: git, failOnce: "body"}
			out := lateRun(t, guard, key, w, lateTasks(t, guard, probe)...)
			require.Equal(t, jobworkflow.JobRunFailed, out.Status)
			require.ErrorContains(t, out.JobError, "rewritten-once")
			require.Equal(t, 1, probe.calls["body"])
			require.Zero(t, probe.calls["continued"])
			_, _, run := lateRecordOutputs(t, rt, key)
			matches := 0
			for _, entry := range run.Execution {
				if entry.Record.Kind == "catch" && entry.Record.Rule != "" {
					matches++
				}
			}
			require.Equal(t, 1, matches, "a deliberate catch failure must not enter the same authored handler twice")
			f.available.Store(false)
			require.Error(t, probeReplay(t, rt, key, w))
		})
	}
}

func TestLateIncludeEnclosingHandlerKeepsRetryPrecedence(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, phase := range []string{"selection", "body", "output"} {
		t.Run(phase, func(t *testing.T) {
			f := newLateGitFixture(t)
			commit := commitHookFile(t, f.dir, "child.yaml", "id: child\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\noutputs: {value: good}\n")
			selector := f.selector("child.yaml", commit)
			if phase == "selection" {
				selector = "local"
			}
			extra := ""
			if phase == "output" {
				extra = "  expect_outputs: {type: object, properties: {value: {type: number}}}\n"
			}
			rec := f.expand(t, fmt.Sprintf("id: root\ncatch:\n- when: 'true'\n  continue: {outputs: {value: enclosing}}\nsequence:\n- id: call\n  include: {bind: late, recipe: %q}\n  retry: {maximum_attempts: 3, initial_interval: 1ms}\n%soutputs: {value: '${{ sequence.call.outputs.value }}'}\n", selector, extra))
			rt := newTimeoutReproRuntime(t, "sqlite")
			guard := lateGuard(rt)
			w := lateWorker(t, rec, nil)
			key := probeSubmit(t, rt, w)
			_, git := GenerateTestContext()
			probe := &scopeProbeTask{calls: map[string]int{}, git: git, failOnce: "body"}
			if phase == "output" {
				probe.failOnce = ""
			}
			out := lateRun(t, guard, key, w, lateTasks(t, guard, probe)...)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
			require.Equal(t, "enclosing", lateJobOutput(t, rt, key)["value"])
			expected := 1
			if phase == "selection" {
				expected = 0
			}
			require.Equal(t, expected, probe.calls["body"])
			f.available.Store(false)
			require.NoError(t, probeReplay(t, rt, key, w))
		})
	}
}
