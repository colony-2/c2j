package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Interrupt only after the backend has accepted an actual task outcome.
type lateInterruption struct {
	jobdb.ConsumerRuntime
	lease  jobdb.ExecutionLease
	phase  string
	cancel context.CancelFunc
	fired  bool
}

func (r *lateInterruption) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	l, err := r.ConsumerRuntime.GetJobLease(ctx, req)
	r.lease = l
	return l, err
}
func lateResultPhase(ch jobdb.Chapter) string {
	body, ok := ch.Body.(jobdb.ExecutionChapter)
	if !ok {
		return ""
	}
	rec, err := jobdb.DecodeExecutionRecord(body.Record)
	if err != nil || rec.Kind != "result" || rec.Output == nil {
		return ""
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(rec.Output.Data, &fields) != nil {
		return ""
	}
	if fields["invocation"] != nil {
		return "select"
	}
	if fields["recipe_yaml_artifact"] != nil {
		return "capture"
	}
	return ""
}
func (r *lateInterruption) PutChapter(ctx context.Context, req jobdb.PutChapterRequest) error {
	if err := r.ConsumerRuntime.PutChapter(ctx, req); err != nil {
		return err
	}
	if !r.fired && lateResultPhase(req.Chapter) == r.phase {
		r.fired = true
		if err := r.lease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "late-include-test"}}); err != nil {
			return err
		}
		r.cancel()
		return fmt.Errorf("worker loss after accepted late %s", r.phase)
	}
	return nil
}
func TestLateIncludeAcceptedBoundariesAndReplay(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"sqlite", "remote"} {
		for _, phase := range []string{"select", "capture"} {
			t.Run(backend+"/"+phase, func(t *testing.T) {
				f := newLateGitFixture(t)
				commit := commitHookFile(t, f.dir, "child.yaml", "id: selected\nversion: '2'\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\noutputs: {value: captured}\n")
				rec := f.expand(t, "id: root\ninput_schema:\n target: {type: string, required: true}\ninputs: {target: '${{ inputs.target }}'}\nsequence:\n- id: call\n  include: {bind: late, recipe: '${{ inputs.target }}'}\noutputs: {value: '{{ sequence.call.outputs.value }}'}\n")
				live := f.selector("child.yaml", commit)
				rt := newTimeoutReproRuntime(t, backend)
				w := lateWorker(t, rec, func() map[string]any { return map[string]any{"target": live} })
				key := probeSubmit(t, rt, w)
				f.beforeRead = func() {
					s, _, _ := lateRecordOutputs(t, rt, key)
					require.Len(t, s, 1, "selection must be accepted before the first Git read")
				}
				ctx, cancel := context.WithCancel(lateTestContext())
				defer cancel()
				interruption := &lateInterruption{ConsumerRuntime: rt, phase: phase, cancel: cancel}
				guard := lateGuard(interruption)
				_, git := GenerateTestContext()
				body := &scopeProbeTask{calls: map[string]int{}, git: git}
				first, err := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: w, TaskWorkers: lateTasks(t, guard, body), WorkerID: "lost-worker", LeaseDuration: time.Minute})
				require.NoError(t, err)
				initial, runErr := first.Run(nil)
				err = runErr
				require.True(t, interruption.fired, "status=%s error=%v run=%v", initial.Status, initial.JobError, err)
				require.Error(t, err)
				require.True(t, interruption.fired)
				require.Empty(t, body.calls)
				s, c, run := lateRecordOutputs(t, rt, key)
				require.Len(t, s, 1)
				if phase == "capture" {
					require.Len(t, c, 1)
				} else {
					require.Empty(t, c)
					require.Zero(t, f.reads.Load())
				}
				for _, e := range run.Execution {
					if e.Record.Kind == "task" && strings.Contains(string(e.Record.Descriptor), lateSelectTaskType) {
						require.Contains(t, string(e.Record.Descriptor), "inputs.target")
						require.NotContains(t, string(e.Record.Descriptor), commit)
						require.NotContains(t, string(e.Record.Descriptor), "selected")
					}
				}
				// A completed selection bypasses even an invalid new live selector.
				live = "not-a-git-selector"
				if phase == "capture" {
					f.available.Store(false)
				}
				guard = lateGuard(rt)
				out := lateRun(t, guard, key, w, lateTasks(t, guard, body)...)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				require.Equal(t, map[string]int{"body": 1}, body.calls)
				afterS, afterC, _ := lateRecordOutputs(t, rt, key)
				require.Equal(t, s, afterS)
				require.Len(t, afterC, 1)
				require.Equal(t, commit, afterS[0].Commit)
				f.available.Store(false)
				reads := f.reads.Load()
				before, err := rt.ListChapters(testBackground(), jobdb.ListChaptersRequest{JobKey: key})
				require.NoError(t, err)
				require.NoError(t, probeReplay(t, rt, key, w))
				require.Equal(t, reads, f.reads.Load())
				require.Equal(t, map[string]int{"body": 1}, body.calls)
				after, err := rt.ListChapters(testBackground(), jobdb.ListChaptersRequest{JobKey: key})
				require.NoError(t, err)
				require.Equal(t, before, after)
			})
		}
	}
}
