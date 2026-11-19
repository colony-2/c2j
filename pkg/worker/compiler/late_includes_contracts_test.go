package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/pkg/artifacts"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"io"
	"testing"

	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func lateJobOutput(t *testing.T, rt jobdb.ConsumerRuntime, key jobdb.JobKey) map[string]any {
	t.Helper()
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).BuildEngine()
	require.NoError(t, err)
	result, err := swfutil.JobResult(lateTestContext(), engine, key)
	require.NoError(t, err)
	var output map[string]any
	require.NoError(t, json.Unmarshal(result.GetDataOrPanic(), &output))
	return output
}

func TestLateIncludeContracts(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, state := range []bool{false, true} {
		for _, mode := range []string{"defaults", "caller-input", "bad-input", "bad-output", "prohibited-root"} {
			t.Run(fmt.Sprintf("state=%t/%s", state, mode), func(t *testing.T) {
				f := newLateGitFixture(t)
				child := "id: selected\ninput_schema:\n value: {type: string, default_value: selected-default}\ninputs: {value: '{{ inputs.value }}'}\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\noutputs: {value: '{{ inputs.value }}'}\n"
				if mode == "prohibited-root" {
					child = "id: prohibited\nop: scope_probe\n"
				}
				commit := commitHookFile(t, f.dir, "child.yaml", child)
				extra := "expect_outputs: {type: object, properties: {value: {type: string}}, required: [value], additionalProperties: false}\n"
				if mode == "caller-input" {
					extra += "inputs: {value: caller}\n"
				}
				if mode == "bad-input" {
					extra += "inputs: {value: 42}\n"
				}
				if mode == "bad-output" {
					extra = "expect_outputs: {type: object, properties: {value: {type: integer}}, required: [value]}\n"
				}
				call := fmt.Sprintf("include: {bind: late, recipe: %q}\n%s", f.selector("child.yaml", commit), extra)
				body := "id: root\nsequence:\n- id: call\n" + indentLate(call, "  ") + "- id: continuation\n  op: scope_probe\n  inputs: {name: continued}\noutputs: {value: '${{ sequence.call.outputs.value }}'}\n"
				if state {
					body = "id: root\nstate:\n initial: call\n states:\n  call:\n" + indentLate(call, "   ") + "   transitions: [{to: continuation}]\n  continuation:\n   op: scope_probe\n   inputs: {name: continued}\noutputs: {value: '${{ states.call.outputs.value }}'}\n"
				}
				rec := f.expand(t, body)
				rt := newTimeoutReproRuntime(t, "sqlite")
				guard := lateGuard(rt)
				w := lateWorker(t, rec, nil)
				key := probeSubmit(t, rt, w)
				_, git := GenerateTestContext()
				probe := &scopeProbeTask{calls: map[string]int{}, git: git}
				out := lateRun(t, guard, key, w, lateTasks(t, guard, probe)...)
				if mode == "defaults" || mode == "caller-input" {
					require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
					expected := "selected-default"
					if mode == "caller-input" {
						expected = "caller"
					}
					require.Equal(t, expected, lateJobOutput(t, rt, key)["value"])
					require.Equal(t, 1, probe.calls["body"])
					require.Equal(t, 1, probe.calls["continued"])
					f.available.Store(false)
					require.NoError(t, probeReplay(t, rt, key, w))
				} else {
					require.Equal(t, jobworkflow.JobRunFailed, out.Status, "%v", out.JobError)
					require.Zero(t, probe.calls["continued"])
					if mode != "bad-output" {
						require.Zero(t, probe.calls["body"])
					}
				}
			})
		}
	}
}

// The native artifact returned by an operation must remain addressable through
// the include's ordinary envelope and the caller's final job result.
func TestLateIncludeNativeArtifactAndContinuation(t *testing.T) {
	op := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "late_artifact"}, func(deps coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
		return map[string]any{}, deps.AddOutputArtifact(jobdb.NewArtifactFromBytes("proof.txt", []byte("native-proof")))
	})
	ws := workspaceTestWorker(t, op)
	f := newLateGitFixture(t)
	commit := commitHookFile(t, f.dir, "child.yaml", "id: child\nsequence:\n- id: effect\n  op: late_artifact\noutputs: {artifact_refs: '${{ sequence.effect.artifacts }}', job: '${{ context.workflow.job_id }}'}\n")
	rec := f.expand(t, fmt.Sprintf("id: root\nsequence:\n- id: call\n  include: {bind: late, recipe: %q}\n- id: continuation\n  op: workspace_probe\n  inputs: {read: identity}\noutputs: {artifact_refs: '${{ sequence.call.outputs.artifact_refs }}', job: '${{ sequence.call.outputs.job }}', identity: '${{ sequence.continuation.outputs.text }}'}\n", f.selector("child.yaml", commit)))
	rt := newTimeoutReproRuntime(t, "sqlite")
	guard := lateGuard(rt)
	w := lateRealWorker(t, rec, f)
	key := probeSubmit(t, rt, w)
	out := lateRun(t, guard, key, w, lateRealTasks(t, guard, ws)...)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
	result := lateJobOutput(t, rt, key)
	require.Equal(t, key.JobId, result["job"])
	require.Equal(t, "A", result["identity"])
	raw, err := json.Marshal(result["artifact_refs"])
	require.NoError(t, err)
	var refs map[string]artifacts.Ref
	require.NoError(t, json.Unmarshal(raw, &refs))
	require.Contains(t, refs, "proof.txt")
	keyRef, ok := refs["proof.txt"].StoredKey()
	require.True(t, ok)
	reader, err := rt.OpenArtifact(context.Background(), jobdb.ArtifactRef{JobKey: key, Ordinal: keyRef.TaskOrdinal, Name: keyRef.Name})
	require.NoError(t, err)
	stream, err := reader.Open()
	require.NoError(t, err)
	defer stream.Close()
	bytes, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.Equal(t, "native-proof", string(bytes))
}
