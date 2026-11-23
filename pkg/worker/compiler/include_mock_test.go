package compiler

import (
	"fmt"
	"testing"

	"github.com/colony-2/c2j/internal/includetest"
	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type includeMockProbe struct {
	test *testing.T
	path string
	runs int
}

func (*includeMockProbe) Name() string { return includetest.TaskType }

func (p *includeMockProbe) Run(_ jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	p.runs++
	raw, err := input.GetData()
	require.NoError(p.test, err)
	var invocation includetest.Invocation
	require.NoError(p.test, jsonexact.Unmarshal(raw, &invocation))
	require.Equal(p.test, p.path, invocation.NodePath)
	require.Equal(p.test, map[string]any{"message": "callee default"}, invocation.Inputs)
	return jobdb.NewTaskData(includetest.Result{Outputs: map[string]any{"value": "mocked"}})
}

func TestLateIncludeMockReplaysRecordedResponse(t *testing.T) {
	const child = `id: selected
version: "1"
input_schema:
  message: {type: string, default_value: callee default}
sequence: []
outputs: {value: actual}
`
	for _, tc := range []struct{ name, path, parent string }{
		{"sequence", "root/[0]", `id: root
sequence:
  - id: call
    include: {bind: late, recipe: %q}
    expect_outputs: {type: object, properties: {value: {const: mocked}}}
outputs: {value: '${{ sequence.call.outputs.value }}'}
`},
		{"state", "root/call", `id: root
state:
  initial: call
  states:
    call:
      include: {bind: late, recipe: %q}
      expect_outputs: {type: object, properties: {value: {const: mocked}}}
outputs: {value: '${{ states.call.outputs.value }}'}
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := newLateGitFixture(t)
			commit := commitHookFile(t, source.dir, "child.yaml", child)
			rec := source.expand(t, fmt.Sprintf(tc.parent, source.selector("child.yaml", commit)))
			rt := newTimeoutReproRuntime(t, "sqlite")
			guard := lateGuard(rt)
			worker := lateWorker(t, rec, nil)
			key := probeSubmit(t, rt, worker)
			probe := &includeMockProbe{test: t, path: tc.path}
			ctx := includetest.WithControl(lateTestContext(), includetest.Control{
				Matches: func(job jobdb.JobKey, path string) bool { return job == key && path == tc.path },
				Reject:  func(_ string, err error) error { return err },
			})
			run, err := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{
				JobKey: key, JobWorker: worker, TaskWorkers: lateTasks(t, guard, probe), WorkerID: "include-mock",
			})
			require.NoError(t, err)
			out, err := run.Run(nil)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
			require.Equal(t, 1, probe.runs)

			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(worker).BuildEngine()
			require.NoError(t, err)
			source.available.Store(false)
			reads := source.reads.Load()
			replayed, err := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key})
			require.NoError(t, err)
			raw, err := replayed.GetData()
			require.NoError(t, err)
			require.JSONEq(t, `{"value":"mocked"}`, string(raw))
			require.Equal(t, 1, probe.runs)
			require.Equal(t, reads, source.reads.Load())
		})
	}
}
