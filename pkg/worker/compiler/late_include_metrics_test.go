package compiler

import (
	"fmt"
	"testing"

	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/metricshistory"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Use the production worker registration: the older late-include fixtures
// intentionally install raw workers so they can also exercise legacy results.
func TestLateIncludeMetricsWithRegisteredWorkers(t *testing.T) {
	for _, tc := range []struct {
		backend string
		state   bool
	}{{"sqlite", false}, {"remote", false}, {"sqlite", true}, {"remote", true}} {
		t.Run(fmt.Sprintf("%s/state=%t", tc.backend, tc.state), func(t *testing.T) {
			ws := workspaceTestWorker(t)
			f := newLateGitFixture(t)
			f.initial = commitHookFile(t, f.dir, ".c2/hooks/check.yaml", "id: check\nsequence: []\noutputs: {value: hook}\n")
			commit := commitHookFile(t, f.dir, "selected.yaml", "id: selected\nsequence:\n- id: hook\n  include: cell://.c2/hooks/check.yaml\noutputs: {value: '${{ sequence.hook.outputs.value }}'}\n")
			body := fmt.Sprintf("sequence:\n- id: call\n  include: {bind: late, recipe: %q}\n", f.selector("selected.yaml", commit))
			binding := "sequence.call"
			if tc.state {
				body = fmt.Sprintf("state:\n initial: call\n states:\n  call:\n   include: {bind: late, recipe: %q}\n", f.selector("selected.yaml", commit))
				binding = "states.call"
			}
			rec := f.expand(t, "id: root\nsource_hooks: from_current\n"+body+fmt.Sprintf(`outputs:
 value: '${{ %s.outputs.value }}'
 job: '${{ context.metrics }}'
 node: '${{ %s.metrics }}'
`, binding, binding))
			rt := newTimeoutReproRuntime(t, tc.backend)
			guard := lateGuard(rt)
			worker := lateRealWorker(t, rec, f)
			worker.ObserveTaskResults = ObserveTaskMetrics
			key := probeSubmit(t, rt, worker)
			var tasks []jobworkflow.TaskWorker
			for _, task := range ws.TaskWorkers {
				tasks = append(tasks, guard.WrapTaskWorker(task))
			}
			values := metricViewOutput(t, lateRun(t, guard, key, worker, tasks...))
			require.Equal(t, "hook", values["value"])
			run, err := jobdb.GetJobRun(lateTestContext(), rt, jobdb.GetJobRunRequest{JobKey: key})
			require.NoError(t, err)
			for _, kind := range []string{lateSelectTaskType, lateCaptureTaskType, hookPolicyTaskType, hookResolveTaskType} {
				requireInternalWorkerMetrics(t, run, kind)
			}
			before, err := metricshistory.Read(lateTestContext(), rt, key, metrics.Query{Deltas: true})
			require.NoError(t, err)
			job := metricViewSnapshot(t, values["job"])
			atOutput, err := metricshistory.Read(lateTestContext(), rt, key, metrics.Query{ThroughOrdinal: &job.Cutoff})
			require.NoError(t, err)
			require.Equal(t, atOutput.Snapshot, job)
			require.Equal(t, before.Snapshot.Series, job.Series)
			require.Equal(t, before.Snapshot.Coverage, job.Coverage)
			node := metricViewSnapshot(t, values["node"])
			for _, kind := range []string{lateSelectTaskType, lateCaptureTaskType} {
				phases := map[string]int64{}
				for _, point := range node.Series["c2j_task_duration_seconds"] {
					if point.Labels["task_type"] == kind {
						phases[point.Labels["phase"]] += point.Count
					}
				}
				require.Equal(t, map[string]int64{"preparation": 1, "execution": 1}, phases, kind)
			}
			f.available.Store(false)
			reads := f.reads.Load()
			require.NoError(t, probeReplay(t, rt, key, worker))
			require.Equal(t, reads, f.reads.Load(), "replay must use captured include and hook results")
			after, err := metricshistory.Read(lateTestContext(), rt, key, metrics.Query{Deltas: true})
			require.NoError(t, err)
			require.Equal(t, before, after, "replay must not append new measurements")
		})
	}
}
