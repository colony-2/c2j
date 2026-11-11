package story_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/objects"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/c2j/pkg/story"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

const largePatchRecipe = `schema_version: 2
id: targeted
input_schema:
  records:
    type: array
    items:
      type: object
      properties:
        id: {type: string}
        b:
          type: object
          properties:
            c: {type: array, items: {type: string}}
        artifact: {type: artifact}
        checkpoint: {type: checkpoint, object_type: fixture/v1}
inputs: {records: '${{ inputs.records }}'}
sequence:
- id: seed
  inputs: {records: '${{ inputs.records }}'}
  sequence: []
  outputs: {records: '${{ inputs.records }}', remove: gone, array: [1, 2, 3]}
- id: observe
  op: restart_context_probe
  inputs: {value: '${{ sequence.seed.outputs.records[4].b.c[2] }}'}
- id: capture
  inputs: {records: '${{ sequence.seed.outputs.records }}', seed: '${{ sequence.seed.outputs }}'}
  sequence: []
  outputs: {records: '${{ inputs.records }}', seed: '${{ inputs.seed }}'}
outputs:
  records: '${{ sequence.capture.outputs.records }}'
  seed: '${{ sequence.capture.outputs.seed }}'
  value: '${{ sequence.observe.outputs.value }}'
`

func largePatchInputs() map[string]any {
	records := make([]any, 256)
	for i := range records {
		records[i] = map[string]any{"id": fmt.Sprintf("record-%d", i), "b": map[string]any{"c": []any{"a", "b", "before"}},
			"artifact":   artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "source", TaskOrdinal: 1, Name: "reference"}),
			"checkpoint": objects.Ref{Format: objects.Version, Type: "fixture/v1", TenantID: "restart-tests", Artifact: jobdb.ArtifactKey{JobId: "source", TaskOrdinal: 1, Name: objects.ArtifactPrefix + strings.Repeat("a", 64) + ".tar"}, SHA256: strings.Repeat("a", 64)}}
	}
	return map[string]any{"records": records}
}

func restartRequest(f *restartFixture, job, scope, id string) story.RestartRecipeJobRequest {
	q := story.RestartRecipeJobRequest{ProjectID: f.key.TenantId, JobID: job, ScopeID: scope, RequestID: id}
	q.Goal.Mode = "new"
	return q
}
func storedPatch(t *testing.T, f *restartFixture, key jobdb.JobKey) json.RawMessage {
	t.Helper()
	run, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: key})
	require.NoError(t, err)
	var payload json.RawMessage
	for _, entry := range run.Execution {
		if entry.Record.Kind == "restart" && len(entry.Record.Payload) > 0 {
			payload = entry.Record.Payload
		}
	}
	require.NotEmpty(t, payload)
	return payload
}

// JP-13, JP-17, JP-19, JP-22: real continuation execution, persisted compact
// arrays, repeat replay, exact request retries, references and source isolation.
func TestRestartJSONPatchLargeArrayContinuation(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			inputs := largePatchInputs()
			f := newRestartFixtureInputs(t, backend, largePatchRecipe, inputs)
			source := f.story(t, f.key)
			scope := storyNodes(source.Root, "op", restartProbeOp)[0].RestartScopeID
			before, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: f.key, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			patchRaw := `[{"op":"test","path":"/sequence/seed/outputs/records/4/id","value":"record-4"},{"op":"replace","path":"/sequence/seed/outputs/records/4/b/c/2","value":"foo"},{"op":"add","path":"/sequence/seed/outputs/records/-","value":{"id":"replacement-job","b":{"c":["x","y","z"]}}}]`
			q := restartRequest(f, f.key.JobId, scope, "targeted-retry")
			q.Patch, err = jobrestart.ParsePatch([]byte(patchRaw))
			require.NoError(t, err)
			result, err := f.service.RestartRecipeJob(f.ctx, q)
			require.NoError(t, err)
			retry, err := f.service.RestartRecipeJob(f.ctx, q)
			require.NoError(t, err)
			require.Equal(t, result, retry)
			originalPatch := *q.Patch
			changed := append(originalPatch[:0:0], originalPatch...)
			changed[0], changed[1] = changed[1], changed[0]
			q.Patch = &changed
			_, err = f.service.RestartRecipeJob(f.ctx, q)
			require.Error(t, err, "changed operation order cannot reuse request ID")
			q.Patch = &originalPatch
			q.ScopeID = storyNodes(source.Root, "sequence", "capture")[0].RestartScopeID
			_, err = f.service.RestartRecipeJob(f.ctx, q)
			require.Error(t, err, "changed selection cannot reuse request ID")
			key := jobdb.JobKey{TenantId: result.TenantID, JobId: result.JobID}
			payload := storedPatch(t, f, key)
			require.JSONEq(t, patchRaw, string(payload))
			require.Less(t, len(payload), 600)
			f.run(t, key)
			restarted := f.story(t, key)
			out := restarted.Root.Output.(map[string]any)
			require.Equal(t, "foo", out["value"])
			records := out["records"].([]any)
			require.Len(t, records, 257)
			expected := largePatchInputs()["records"].([]any)
			expected[4].(map[string]any)["b"].(map[string]any)["c"].([]any)[2] = "foo"
			for i := range expected {
				want, _ := json.Marshal(expected[i])
				got, _ := json.Marshal(records[i])
				require.JSONEq(t, string(want), string(got), "record %d", i)
			}
			require.Equal(t, "replacement-job", records[256].(map[string]any)["id"])
			require.Equal(t, int32(2), f.task.calls.Load())
			// A fresh replay reads the durable patch; it must not append again or run tasks.
			require.Equal(t, restarted.Root.Output, f.story(t, key).Root.Output)
			require.Equal(t, int32(2), f.task.calls.Load())
			require.Equal(t, []byte(payload), []byte(storedPatch(t, f, key)))
			after, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: f.key, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Equal(t, "before", inputs["records"].([]any)[4].(map[string]any)["b"].(map[string]any)["c"].([]any)[2])
		})
	}
}

// JP-18, JP-20, JP-21: historical wire objects go straight to the backend as
// retained fixtures, never through the new writer. Both readers must replay the
// old job keys, merge null deletion and array replacement at retained boundaries.
func TestRestartStoredLegacyMergeCompatibility(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newRestartFixtureInputs(t, backend, largePatchRecipe, largePatchInputs())
			scope := storyNodes(f.story(t, f.key).Root, "op", restartProbeOp)[0].RestartScopeID
			legacy := json.RawMessage(`{"job":{"git":{"author":"historical-author"}},"scopes":[{"container":"sequence","id":"seed","outputs":{"remove":null,"array":[9]}}]}`)
			accepted, err := f.runtime.SubmitRestartJob(f.ctx, jobdb.SubmitRestartJobRequest{RequestID: "pre-upgrade-accepted", Job: jobdb.SubmitRestartJob{Cell: "test", PriorJobKey: f.key, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: scope, Payload: legacy}}})
			require.NoError(t, err)
			original := append([]byte(nil), storedPatch(t, f, accepted.JobKey)...)
			// Even an exact accepted legacy retry is rejected by today's public decoder.
			var q jobrestart.Request
			request := fmt.Sprintf(`{"operation":"submit","job_id":%q,"selector":{"scope":%q},"request_id":"pre-upgrade-accepted","patch":%s}`, f.key.JobId, scope, legacy)
			require.ErrorContains(t, json.Unmarshal([]byte(request), &q), "operation array")
			require.True(t, bytes.Equal(original, storedPatch(t, f, accepted.JobKey)))
			f.run(t, accepted.JobKey)
			st := f.story(t, accepted.JobKey)
			seed := st.Root.Output.(map[string]any)["seed"].(map[string]any)
			require.NotContains(t, seed, "remove")
			require.Equal(t, []any{float64(9)}, seed["array"])
			last := storyNodes(st.Root, "sequence", "capture")[0].RestartScopeID
			require.Equal(t, "historical-author", f.inspectScope(t, accepted.JobKey, last).Job["git"].(map[string]any)["author"])
			retained, err := f.service.RestartRecipeJob(f.ctx, restartRequest(f, accepted.JobKey.JobId, last, "retain-historical"))
			require.NoError(t, err)
			retainedKey := jobdb.JobKey{TenantId: retained.TenantID, JobId: retained.JobID}
			f.run(t, retainedKey)
			require.Equal(t, st.Root.Output, f.story(t, retainedKey).Root.Output)
			require.True(t, bytes.Equal(original, storedPatch(t, f, accepted.JobKey)))
		})
	}
}
