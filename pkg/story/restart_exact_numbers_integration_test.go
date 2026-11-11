package story_test

import (
	"encoding/json"
	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

const exactValues = `{"n":9007199254740993,"adjacent":9007199254740992,"nested":[{"n":9007199254740995},18446744073709551617,1.23000000000000000001]}`
const exactHistoryRecipe = `schema_version: 2
id: exact
input_schema:
  values: {type: object, additionalProperties: true}
inputs: {values: '${{ inputs.values }}'}
sequence:
 - id: durable
   op: restart_context_probe
   inputs: {value: exact-number-fixture}
 - id: seed
   inputs: {values: '${{ inputs.values }}'}
   sequence: []
   outputs: {values: '${{ inputs.values }}', unrelated: before}
 - id: machine
   inputs: {values: '${{ inputs.values }}'}
   state:
     initial: first
     states:
       first:
         inputs: {values: '${{ inputs.values }}'}
         sequence: []
         outputs: {values: '${{ inputs.values }}'}
         transitions: [{to: second}]
       second:
         sequence:
          - id: target
            op: restart_context_probe
            inputs: {value: target}
   outputs: {values: '${{ states.first.outputs.values }}'}
 - id: last
   op: restart_context_probe
   inputs: {value: last}
outputs: {values: '${{ sequence.seed.outputs.values }}', durable: '${{ sequence.durable.outputs.exact }}', inline: '${{ context.artifacts }}', seed: '${{ sequence.seed.outputs }}', statevalues: '${{ sequence.machine.outputs.values }}'}
`

func assertExactValues(t *testing.T, value any) {
	t.Helper()
	assertExactJSON(t, exactValues, value)
}

func assertExactJSON(t *testing.T, expected string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	var got, want any
	require.NoError(t, jsonexact.Unmarshal(raw, &got))
	require.NoError(t, jsonexact.Unmarshal([]byte(expected), &want))
	require.Equal(t, want, got, "raw JSON: %s", raw)
}

// NUM-02/03: durable SQLite and HTTP histories, both restart payload versions,
// prior sequence and state outputs, submitted inline artifact, and fresh replay.
func TestRestartExactNumberHistory(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			var values map[string]any
			require.NoError(t, jsonexact.Unmarshal([]byte(exactValues), &values))
			jc := contextual.JobContext{Artifacts: map[string]artifacts.Ref{"exact": {Kind: artifacts.RefKindValue, Format: "json", Value: json.RawMessage(exactValues)}}}
			f := newRestartFixtureContext(t, backend, exactHistoryRecipe, map[string]any{"values": values}, jc, "")
			submission := f.inspect(t, f.key, 0)
			assertExactValues(t, submission.Job["artifacts"].(map[string]any)["exact"].(map[string]any)["value"])
			source := f.story(t, f.key)
			ops := storyNodes(source.Root, "op", restartProbeOp)
			require.Len(t, ops, 3)
			snapshot := f.inspectScope(t, f.key, storyNodes(source.Root, "state", "second")[0].RestartScopeID)
			state := snapshot.Visible["states"].(map[string]any)["first"].(map[string]any)
			assertExactValues(t, state["outputs"].(map[string]any)["values"])
			lastScope := ops[2].RestartScopeID
			check := func(key jobdb.JobKey) {
				for range 2 { // each call reconstructs history with a fresh read-only worker
					snap := f.inspectScope(t, key, lastScope)
					seed := snap.Visible["sequence"].(map[string]any)["seed"].(map[string]any)["outputs"].(map[string]any)
					assertExactValues(t, seed["values"])
					assertExactValues(t, snap.Visible["sequence"].(map[string]any)["durable"].(map[string]any)["outputs"].(map[string]any)["exact"])
					assertExactValues(t, snap.Job["artifacts"].(map[string]any)["exact"].(map[string]any)["value"])
				}
			}
			check(f.key)
			patch, err := jobrestart.ParsePatch([]byte(`[{"op":"test","path":"/sequence/seed/outputs/values/n","value":9007199254740993},{"op":"replace","path":"/sequence/seed/outputs/unrelated","value":"after"},{"op":"add","path":"/context/git/author","value":"exact-author"}]`))
			require.NoError(t, err)
			q := restartRequest(f, f.key.JobId, ops[0].RestartScopeID, "exact-patch")
			// The seed is in the parent scope, so use the outer last operation boundary.
			q.ScopeID = lastScope
			q.Patch = patch
			result, err := f.service.RestartRecipeJob(f.ctx, q)
			require.NoError(t, err)
			patchedKey := jobdb.JobKey{TenantId: result.TenantID, JobId: result.JobID}
			f.run(t, patchedKey)
			// Inspect after the patched boundary by adding an independent restart later
			// is unnecessary: final outputs expose retained seed values on fresh replay.
			for range 2 {
				out := f.story(t, patchedKey).Root.Output.(map[string]any)
				assertExactValues(t, out["values"])
				assertExactValues(t, out["durable"])
				assertExactValues(t, out["statevalues"])
				assertExactValues(t, json.RawMessage(out["inline"].(map[string]artifacts.Ref)["exact"].Value))
			}
			// A mismatch one integer away fails before the selected operation executes.
			wrong := restartRequest(f, f.key.JobId, lastScope, "adjacent-mismatch")
			wrong.Patch, err = jobrestart.ParsePatch([]byte(`[{"op":"replace","path":"/sequence/seed/outputs/unrelated","value":"must-not-leak"},{"op":"test","path":"/sequence/seed/outputs/values/n","value":9007199254740992}]`))
			require.NoError(t, err)
			failed, err := f.service.RestartRecipeJob(f.ctx, wrong)
			require.NoError(t, err)
			failedKey := jobdb.JobKey{TenantId: failed.TenantID, JobId: failed.JobID}
			calls := f.task.calls.Load()
			run, err := jobworkflow.GetJobForRun(f.ctx, f.runtime, jobworkflow.GetJobForRunRequest{JobKey: failedKey, JobWorker: f.worker, TaskWorkers: []jobworkflow.TaskWorker{compiler.NewCellContextTaskWorker(), f.task}, WorkerID: "exact-negative", LeaseDuration: time.Minute})
			require.NoError(t, err)
			outcome, err := run.Run(nil)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunFailed, outcome.Status)
			job, err := f.runtime.GetJob(f.ctx, failedKey)
			require.NoError(t, err)
			require.Contains(t, job.CompletionDetail, "test failed")
			require.Equal(t, calls, f.task.calls.Load())
			check(f.key)
			legacy := json.RawMessage(`{"job":{"git":{"author":"legacy-exact"}},"scopes":[{"container":"sequence","id":"seed","outputs":{"unrelated":"legacy","added":` + exactValues + `}}]}`)
			accepted, err := f.runtime.SubmitRestartJob(f.ctx, jobdb.SubmitRestartJobRequest{RequestID: "exact-legacy", Job: jobdb.SubmitRestartJob{Cell: "test", PriorJobKey: f.key, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: lastScope, Payload: legacy}}})
			require.NoError(t, err)
			f.run(t, accepted.JobKey)
			for range 2 {
				out := f.story(t, accepted.JobKey).Root.Output.(map[string]any)
				assertExactValues(t, out["values"])
				assertExactValues(t, out["durable"])
				assertExactValues(t, out["statevalues"])
				assertExactValues(t, out["seed"].(map[string]any)["added"])
				assertExactValues(t, json.RawMessage(out["inline"].(map[string]artifacts.Ref)["exact"].Value))
			}
		})
	}
}
