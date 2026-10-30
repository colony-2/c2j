package compiler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Fixed seeds make failures reproducible. Include Unicode, whitespace, escapes,
// and expression-looking content so the hash detects unintended rewriting.
func randomArtifactSample(seed int64) string {
	rng := rand.New(rand.NewSource(seed))
	alphabet := []rune("abcXYZ0129 λ🦊漢字\n\r\t\\\"'<>/{}[]")
	text := make([]rune, 16384)
	for i := range text {
		text[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return string(text) + "\n${{ nonexistent.value }}\n$(false)\n"
}
func artifactDigestOp(calls *atomic.Int32) coreops.RegisterableOp {
	return coreops.NewActivityMappedOpV2[struct {
		Path string `json:"path"`
	}, map[string]any](coreops.OpMetadata{Type: "value_digest", AcceptsArtifacts: true}, func(_ coreops.OpDependencies, _ context.Context, in struct {
		Path string `json:"path"`
	}) (map[string]any, error) {
		data, err := os.ReadFile(in.Path)
		if err != nil {
			return nil, err
		}
		calls.Add(1)
		return map[string]any{"sha256": fmt.Sprintf("%x", sha256.Sum256(data)), "size": len(data)}, nil
	})
}

func TestValueArtifactPersistedHandoffRecovery(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			sample := randomArtifactSample(71)
			body := strings.Repeat(sample, 16)
			valueExpr := `${{ jq(inputs.sample, '. * 16') }}`
			expected := []byte(body)
			if format == "json" {
				valueExpr = `${{ {'body': jq(inputs.sample, '. * 16'), 'values': [null, true, 42, '']} }}`
				var err error
				expected, err = json.Marshal(map[string]any{"body": body, "values": []any{nil, true, 42, ""}})
				require.NoError(t, err)
				expected = append(expected, '\n')
			}
			var prefixCalls, consumerCalls atomic.Int32
			prefix := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "value_prefix"}, func(coreops.OpDependencies, context.Context, struct{}) (map[string]any, error) {
				prefixCalls.Add(1)
				return map[string]any{}, nil
			})
			workspaceTestWorker(t, prefix, artifactDigestOp(&consumerCalls))
			repo := t.TempDir()
			workspaceRepo(t, repo, "A")
			db := filepath.Join(t.TempDir(), "jobs.db")
			rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: db})
			require.NoError(t, err)
			closed := false
			t.Cleanup(func() {
				if !closed {
					_ = rt.Close(ctx)
				}
			})
			guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
			eng, err := jobworkflow.NewEngineBuilder().WithRuntime(guard).WithWorkerTenantId("object-tests").BuildEngine()
			require.NoError(t, err)
			eng = jobdbschema.WorkflowEngine{Engine: eng, Registry: rt}
			registry, err := workerops.NewActivityRegistry()
			require.NoError(t, err)
			ws, err := NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().Build(), registry, RecipeJobWorkerOptions{WrapTaskWorker: guard.WrapTaskWorker})
			require.NoError(t, err)
			require.NoError(t, eng.RegisterWorkers(ws))
			rec, err := recipe.LoadRecipeFromString([]byte(fmt.Sprintf(`id: handoff
input_schema:
 sample: {type: string, required: true}
inputs: {sample: '${{ inputs.sample }}'}
sequence:
 - id: prefix
   op: value_prefix
 - id: consume
   op: value_digest
   artifacts:
    document:
     kind: value
     format: %s
     value: %q
   inputs: {path: '${{ context.environment.inbox }}/document'}
outputs:
 digest: '${{ sequence.consume.outputs }}'
`, format, valueExpr)))
			require.NoError(t, err)
			job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
			ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "object-tests", RecipeName: "handoff", JobContext: job, GitRef: "main", Inputs: map[string]any{"sample": sample}}, eng, *rec)
			require.NoError(t, err)
			key := ref.Key()
			// Leave the consumer route unserved so JobDB records its input and
			// suspends before the consumer can execute.
			var workers []jobworkflow.TaskWorker
			for name, worker := range ws.TaskWorkers {
				if name != "value_digest:value_digest" {
					workers = append(workers, worker)
				}
			}
			runnable, err := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: workers, WorkerID: "handoff", LeaseDuration: time.Minute})
			require.NoError(t, err)
			outcome, err := runnable.Run(nil)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunSuspended, outcome.Status, "%v", outcome.JobError)
			pending, err := eng.GetWaitingTask(ctx, key)
			require.NoError(t, err)
			require.Equal(t, "value_digest:value_digest", pending.TaskType())
			input, err := pending.Data()
			require.NoError(t, err)
			envelope, err := input.GetData()
			require.NoError(t, err)
			require.Less(t, len(envelope), 128*1024)
			var invocation workerops.ActivityInvocationRequest
			require.NoError(t, json.Unmarshal(envelope, &invocation))
			require.NotEmpty(t, invocation.InputArtifact)
			attachments, err := input.GetArtifacts()
			require.NoError(t, err)
			var inputKey jobdb.ArtifactKey
			for _, a := range attachments {
				if a.Name() == invocation.InputArtifact {
					inputKey, err = a.ArtifactKey()
					require.NoError(t, err, "attachment must be persisted before replacement")
				}
			}
			require.Equal(t, key.JobId, inputKey.JobId)
			require.Greater(t, inputKey.SizeBytes, int64(128*1024))
			require.EqualValues(t, 1, prefixCalls.Load())
			require.Zero(t, consumerCalls.Load())
			require.NoError(t, rt.Close(ctx))
			closed = true
			// No original runtime, worker, or unpersisted TaskData is used after this.
			eng, _ = objectEngineAt(t, db)
			require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 15*time.Second, key, eng))
			result, err := swfutil.JobResult(ctx, eng, key)
			require.NoError(t, err)
			raw, err := result.GetData()
			require.NoError(t, err)
			var out struct {
				Digest struct {
					SHA256 string `json:"sha256"`
					Size   int    `json:"size"`
				} `json:"digest"`
			}
			require.NoError(t, json.Unmarshal(raw, &out))
			require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(expected)), out.Digest.SHA256)
			require.Equal(t, len(expected), out.Digest.Size)
			history, err := eng.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeArtifacts: true})
			require.NoError(t, err)
			seen := map[string]bool{}
			for _, entry := range history.Execution {
				if entry.Record.Input == nil {
					continue
				}
				for _, a := range entry.Record.Input.Artifacts {
					if a.Name == invocation.InputArtifact {
						require.Equal(t, &inputKey, a.Key, "recovery must reuse the recorded input attachment")
						seen[entry.Record.Kind] = true
					}
				}
			}
			require.True(t, seen["wait"])
			require.True(t, seen["result"])
			_, err = eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key})
			require.NoError(t, err)
			require.EqualValues(t, 1, prefixCalls.Load(), "completed prefix must replay")
			require.EqualValues(t, 1, consumerCalls.Load(), "consumer runs once after recovery")
		})
	}
}

func TestValueArtifactChildExportConsumedByParent(t *testing.T) {
	var calls atomic.Int32
	workspaceTestWorker(t, artifactDigestOp(&calls))
	repo := t.TempDir()
	workspaceRepo(t, repo, "A")
	child := `id: child
input_schema:
 sample: {type: string, required: true}
inputs: {sample: '${{ inputs.sample }}'}
sequence: []
outputs:
 artifact_refs:
  export.json:
   kind: value
   format: json
   value: "${{ {'body': jq(inputs.sample, '. * 16'), 'empty': [], 'nothing': null} }}"
`
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".c2", "recipes"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".c2", "recipes", "child.yaml"), []byte(child), 0644))
	require.NoError(t, runGit(repo, "git", "add", "."))
	require.NoError(t, runGit(repo, "git", "commit", "-m", "child export fixture"))
	sample := randomArtifactSample(97)
	expected, err := json.Marshal(map[string]any{"body": strings.Repeat(sample, 16), "empty": []any{}, "nothing": nil})
	require.NoError(t, err)
	expected = append(expected, '\n')
	eng := objectEngine(t)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	out := runObjectRecipe(t, eng, `id: parent
input_schema:
 sample: {type: string, required: true}
inputs: {sample: '${{ inputs.sample }}'}
sequence:
 - id: submit
   op: recipes.run
   inputs:
    recipes: [{name: child, inputs: {sample: '${{ inputs.sample }}'}}]
 - id: await
   op: recipe.await_result_soft
   inputs: {target: {job: '${{ sequence.submit.outputs.job_ids[0] }}'}, wait: {on: job}}
 - id: consume
   op: value_digest
   artifacts:
    received.json: '${{ sequence.await.outputs.artifacts["export.json"] }}'
   inputs: {path: '${{ context.environment.inbox }}/received.json'}
outputs:
 child: '${{ sequence.submit.outputs.job_ids[0] }}'
 artifact: '${{ sequence.await.outputs.artifacts["export.json"] }}'
 digest: '${{ sequence.consume.outputs }}'
`, job, map[string]any{"sample": sample})
	raw, err := json.Marshal(out["artifact"])
	require.NoError(t, err)
	var ref artifacts.Ref
	require.NoError(t, json.Unmarshal(raw, &ref))
	stored, ok := ref.StoredKey()
	require.True(t, ok, "parent must receive a stored reference, not an export marker")
	require.Equal(t, out["child"], stored.JobId)
	require.Equal(t, "export.json", stored.Name)
	digest := out["digest"].(map[string]any)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(expected)), digest["sha256"])
	require.EqualValues(t, len(expected), digest["size"])
	require.EqualValues(t, 1, calls.Load())
}
