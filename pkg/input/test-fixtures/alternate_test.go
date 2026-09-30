package test_fixtures_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestUnansweredRecipeLifecycle(t *testing.T) {
	ensureFixtureOps()
	for _, backend := range []string{"sqlite", "remote"} {
		for _, mode := range []string{"fallback", "human-late", "review", "structured"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				source := `id: unanswered
version: "1.0.0"
input_schema:
  fallback: {type: string, required: true}
  delay: {type: string, required: true}
inputs:
  fallback: "${{ inputs.fallback }}"
  delay: "${{ inputs.delay }}"
sequence:
  - id: write
    op: command_execution
    inputs:
      run: 'echo preserved > proof.txt'
  - id: decision
    op: input
    inputs:
      form:
        title: Decision
        fields:
          - id: decision
            type: multiple_choice
            question: Proceed?
            required: true
            options: [{value: approve}, {value: defer}]
      if_unanswered:
        after: "${{ inputs.delay }}"
        fields:
          decision: '${{ inputs.fallback }}'
  - id: after
    op: command_execution
    inputs:
      run: cat proof.txt
outputs:
  answers: '${{ sequence.decision.outputs.fields }}'
  preserved: '${{ sequence.after.outputs.stdout }}'
`
				if mode == "review" {
					source = strings.Replace(source, "        title: Decision", `        kind: review
        documents:
          design: '${{ sequence.write.artifacts["review.md"] }}'
        title: Decision`, 1)
					source = strings.Replace(source, "run: 'echo preserved > proof.txt'", `run: 'echo preserved > proof.txt; echo document > "${{ context.environment.op.outbox }}/review.md"'`, 1)
					source = strings.Replace(source, "      if_unanswered:", `          - id: annotated
            type: file_upload
            question: Optional document
      if_unanswered:`, 1)
					source = strings.Replace(source, "          decision: '${{ inputs.fallback }}'", `          decision: '${{ inputs.fallback }}'
          annotated: '${{ sequence.write.artifacts["review.md"] }}'`, 1)
					source = strings.Replace(source, "  - id: after\n    op: command_execution", `  - id: after
    op: command_execution
    artifacts:
      review.md: '${{ sequence.decision.outputs.fields.annotated }}'`, 1)
					source = strings.Replace(source, "run: cat proof.txt", `run: 'cat proof.txt; cat "${{ context.environment.op.inbox }}/review.md"'`, 1)
				}
				if mode == "structured" {
					start := strings.Index(source, "        title: Decision")
					end := strings.Index(source, "      if_unanswered:")
					source = source[:start] + `        response_schema:
          type: object
          required: [decision]
          properties:
            decision: {enum: [approve, defer]}
` + source[end:]
					source = strings.Replace(source, "        fields:\n          decision:", "        response:\n          decision:", 1)
					source = strings.Replace(source, "sequence.decision.outputs.fields", "sequence.decision.outputs.response", 1)
				}
				if mode != "human-late" {
					source += "  receipt: '${{ sequence.decision.outputs.receipt }}'\n"
				}
				primary := filepath.Join(t.TempDir(), "unanswered.yaml")
				require.NoError(t, os.WriteFile(primary, []byte(source), 0600))
				repo, hash := createFixtureRepo(t, primary, nil)
				rec, err := recipe.LoadRecipeFromString([]byte(source))
				require.NoError(t, err)
				ctl := &workflow.SWFWorkflowControl{PreferRuntimeRecipeResolution: true}
				ctl.Registry, err = buildRecipeRegistry(primary, rec, nil, repo, hash)
				require.NoError(t, err)
				resolver := compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{RecipeRefResolver: compiler.NewProviderBackedRecipeRefResolver(func(tenant, ref string) (*recipe.Recipe, error) { return ctl.Registry(tenant, ref) })})
				deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(ctl).Build()
				registry, err := workerops.NewActivityRegistry()
				require.NoError(t, err)
				workers, err := compiler.NewRecipeWorkerWithOptions(deps, registry, compiler.RecipeJobWorkerOptions{RootSourceResolver: resolver})
				require.NoError(t, err)
				tasks := []jobworkflow.TaskWorker{}
				for _, w := range workers.TaskWorkers {
					tasks = append(tasks, w)
				}
				dbPath := filepath.Join(t.TempDir(), "jobs.db")
				store, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: dbPath})
				require.NoError(t, err)
				t.Cleanup(func() { _ = store.Close(ctx) })
				var rt jobdb.WorkflowRuntime = store
				if backend == "remote" {
					server := httptest.NewServer(remote.NewServer(store))
					t.Cleanup(server.Close)
					rt, err = remote.New(server.URL, server.Client())
					require.NoError(t, err)
				}
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, err)
				engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: rt.(jobdb.JobSchemaRegistry)}
				ctl.Engine = engine
				jobCtx, gitCtx := generateTestContext(repo, hash, nil, nil)
				key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "alternate", RecipeName: rec.GetMetadata().ID, Inputs: map[string]any{"fallback": "defer", "delay": "600ms"}, JobContext: jobCtx, GitRef: gitCtx.ParentRef}, engine, *rec)
				require.NoError(t, err)
				run := func() jobworkflow.JobRunOutcome {
					runnable, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: workers.JobWorker, TaskWorkers: tasks, WorkerID: "recipe-worker", LeaseDuration: time.Minute})
					require.NoError(t, err)
					out, err := runnable.Run(nil)
					require.NoError(t, err)
					return out
				}
				first := run()
				require.Equal(t, jobworkflow.JobRunSuspended, first.Status, "%+v", first)
				client, err := input.NewRuntime(ctl, nil)
				require.NoError(t, err)
				form, err := client.GetForm(ctx, key.TenantId, key.JobId)
				require.NoError(t, err)
				at, err := time.Parse(time.RFC3339Nano, form.FallbackAt)
				require.NoError(t, err)
				require.False(t, run().LeaseAcquired, "alternate worker must not execute early")
				details, err := client.GetDetails(ctx, key.TenantId, key.JobId)
				require.NoError(t, err)
				require.NotNil(t, details.Form.FallbackAt)
				require.Equal(t, at, *details.Form.FallbackAt)
				// Reopen persistent storage before resuming the same prepared request.
				if backend == "sqlite" {
					require.NoError(t, store.Close(ctx))
					store, err = sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: dbPath})
					require.NoError(t, err)
					rt = store
					engine, err = jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
					require.NoError(t, err)
					ctl.Engine = engine
				}
				// A new invocation replays preparation; it cannot re-evaluate the template or restart the delay.
				if delay := time.Until(at.Add(20 * time.Millisecond)); delay > 0 {
					time.Sleep(delay)
				}
				frozen, err := client.GetForm(ctx, key.TenantId, key.JobId)
				require.NoError(t, err)
				require.Equal(t, form.FallbackAt, frozen.FallbackAt)
				expected := "defer"
				if mode == "human-late" {
					require.NoError(t, client.SubmitResponse(ctx, key.TenantId, key.JobId, input.FormResponse{Fields: map[string]any{"decision": "approve"}}))
					expected = "approve"
				}
				final := run()
				require.Equal(t, jobworkflow.JobRunCompleted, final.Status, "%+v error=%v", final, final.JobError)
				output, err := ctl.JobResult(ctx, key)
				require.NoError(t, err)
				result, err := readJobOutputAsMap(output)
				require.NoError(t, err)
				require.Equal(t, expected, result["answers"].(map[string]any)["decision"])
				preserved := "preserved"
				if mode == "review" {
					preserved += "\ndocument"
				}
				require.Equal(t, preserved, strings.TrimSpace(fmt.Sprint(result["preserved"])))
				if mode != "human-late" {
					receipt := result["receipt"].(map[string]any)
					require.Equal(t, "if-unanswered:"+form.RequestID, receipt["submission_id"])
				}
				// Durable collection results retain the logical input identity under schema validation.
				chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
				require.NoError(t, err)
				raw, err := json.Marshal(chapters)
				require.NoError(t, err)
				require.Contains(t, string(raw), "input:collect_user_input")
			})
		}
	}
}
