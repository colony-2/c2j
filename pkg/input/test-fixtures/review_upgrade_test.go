package test_fixtures_test

// This harness intentionally uses only the pre-presentation API. The same file
// is compiled against the named baseline to produce real persistent state, then
// against the candidate to consume it. Do not replace this with candidate-made
// legacy-shaped forms: those do not test an upgrade.
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
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
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

const presentationBaseline = "5050cdb6127a143ba0a931a076048299ab0d47c2"

type presentationUpgradeRecord struct {
	Baseline string
	Mode     string
	Source   string
	Repo     string
	Hash     string
	Key      jobdb.JobKey
	Form     json.RawMessage
	Details  json.RawMessage
	Result   map[string]any
	Receipt  *input.Receipt
}

func TestReviewPresentationUpgrade(t *testing.T) {
	if testing.Short() {
		t.Skip("two-version persistent runtime integration")
	}
	baselineDir := t.TempDir()
	archive := filepath.Join(t.TempDir(), "baseline.tar")
	cmd := exec.Command("git", "archive", "--format=tar", "-o", archive, presentationBaseline)
	cmd.Dir = "../../.."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	out, err = exec.Command("tar", "-xf", archive, "-C", baselineDir).CombinedOutput()
	require.NoError(t, err, string(out))
	harness, err := os.ReadFile("review_upgrade_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(baselineDir, "pkg/input/test-fixtures/review_upgrade_test.go"), harness, 0600))
	state := t.TempDir()
	cmd = exec.Command("go", "test", "./pkg/input/test-fixtures", "-run", "^TestReviewPresentationUpgradePhase$", "-count=1", "-v")
	cmd.Dir = baselineDir
	cmd.Env = append(os.Environ(), "C2J_PRESENTATION_UPGRADE_STATE="+state)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	t.Logf("producer revision %s; baseline procedure:\n%s", presentationBaseline, out)
	presentationUpgradePhase(t, state, false)
}

// Invoked only in the baseline subprocess. No feature types may be used here.
func TestReviewPresentationUpgradePhase(t *testing.T) {
	state := os.Getenv("C2J_PRESENTATION_UPGRADE_STATE")
	if state == "" {
		t.Skip("baseline producer subprocess only")
	}
	presentationUpgradePhase(t, state, true)
}

func presentationUpgradePhase(t *testing.T, state string, produce bool) {
	ensureFixtureOps()
	for _, name := range []string{"review", "ordinary", "structured", "queued", "continuation", "completed", "fallback", "timeout"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dir := filepath.Join(state, name)
			var record presentationUpgradeRecord
			if produce {
				require.NoError(t, os.MkdirAll(dir, 0700))
				mode := name
				if mode != "ordinary" && mode != "structured" {
					mode = "review"
				}
				if name == "fallback" {
					mode = "ordinary"
				}
				source := timedInputSource("none", mode, "", mode == "review")
				if name == "timeout" {
					source = timedInputSource("input-op", mode, "1s", true)
				}
				if mode == "review" || name == "fallback" {
					source = strings.Replace(source, "outputs:\n", "outputs:\n  receipt: '${{ sequence.question.outputs.receipt }}'\n", 1)
				}
				if mode == "review" {
					source = strings.Replace(source, "outputs:\n", "outputs:\n  returned_refs: '${{ sequence.question.outputs.artifact_refs }}'\n", 1)
				}
				if name == "continuation" {
					source = strings.Replace(source, "outputs:\n", `  - id: second
    op: input
    inputs:
      form:
        kind: review
        question: Second occurrence?
        type: boolean
outputs:
  first_receipt: '${{ sequence.question.outputs.receipt }}'
`, 1)
				}
				if name == "fallback" {
					source = strings.Replace(source, "    inputs:\n      form:", "    inputs:\n      if_unanswered: {after: 1ns, response: fallback}\n      form:", 1)
				}
				sourcePath := filepath.Join(dir, "source.yaml")
				require.NoError(t, os.WriteFile(sourcePath, []byte(source), 0600))
				repo, hash := createFixtureRepo(t, sourcePath, nil)
				persistentRepo := filepath.Join(dir, "repo")
				require.NoError(t, os.Rename(repo, persistentRepo))
				record = presentationUpgradeRecord{Baseline: presentationBaseline, Mode: mode, Source: source, Repo: persistentRepo, Hash: hash}
			} else {
				raw, err := os.ReadFile(filepath.Join(dir, "observations.json"))
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &record))
				require.Equal(t, presentationBaseline, record.Baseline)
			}
			rec, err := recipe.LoadRecipeFromString([]byte(record.Source))
			require.NoError(t, err)
			ctl := &workflow.SWFWorkflowControl{}
			ctl.Registry, err = buildRecipeRegistry(filepath.Join(dir, "source.yaml"), rec, nil, record.Repo, record.Hash)
			require.NoError(t, err)
			resolver := compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{RecipeRefResolver: compiler.NewProviderBackedRecipeRefResolver(func(tenant, ref string) (*recipe.Recipe, error) { return ctl.Registry(tenant, ref) })})
			store, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(dir, "jobs.db")})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close(ctx)) })
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(store).BuildEngine()
			require.NoError(t, err)
			ctl.Engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: store}
			deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(ctl).Build()
			registry, err := workerops.NewActivityRegistry()
			require.NoError(t, err)
			workers, err := compiler.NewRecipeWorkerWithOptions(deps, registry, compiler.RecipeJobWorkerOptions{RootSourceResolver: resolver})
			require.NoError(t, err)
			if produce {
				jobCtx, gitCtx := generateTestContext(record.Repo, record.Hash, nil, nil)
				key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "presentation-upgrade", RecipeName: rec.GetMetadata().ID, JobContext: jobCtx, GitRef: gitCtx.ParentRef}, ctl.Engine, *rec)
				require.NoError(t, err)
				record.Key = key.Key()
			}
			guard := executionruntime.New(store, execution.Allocation{SchemaVersion: 1})
			tasks := make([]jobworkflow.TaskWorker, 0, len(workers.TaskWorkers))
			for _, task := range workers.TaskWorkers {
				tasks = append(tasks, guard.WrapTaskWorker(task))
			}
			run := func() jobworkflow.JobRunOutcome {
				runnable, err := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: record.Key, JobWorker: workers.JobWorker, TaskWorkers: tasks, WorkerID: "upgrade", LeaseDuration: time.Minute})
				require.NoError(t, err)
				result, err := runnable.Run(nil)
				require.NoError(t, err)
				return result
			}
			client, err := input.NewRuntime(ctl, nil)
			require.NoError(t, err)
			readForm := func() input.InputForm {
				form, err := client.GetForm(ctx, record.Key.TenantId, record.Key.JobId)
				require.NoError(t, err)
				return form
			}
			submit := func(form input.InputForm) *input.Receipt {
				actor := input.Actor{ID: "reviewer", Kind: "human"}
				switch record.Mode {
				case "review":
					_, err := client.SubmitFormResponse(ctx, record.Key.TenantId, record.Key.JobId, input.FormSubmission{RequestID: form.RequestID, SubmissionID: "invalid", Fields: map[string]any{}}, actor)
					require.Error(t, err)
					_, err = client.SubmitFormResponse(ctx, "wrong-tenant", record.Key.JobId, input.FormSubmission{RequestID: form.RequestID, SubmissionID: "wrong"}, actor)
					require.Error(t, err)
					_, err = client.OpenReviewDocument(ctx, record.Key.TenantId, record.Key.JobId, form.RequestID, "unattached")
					require.Error(t, err)
					opened, err := client.OpenReviewDocument(ctx, record.Key.TenantId, record.Key.JobId, form.RequestID, "design")
					require.NoError(t, err)
					data, err := io.ReadAll(opened)
					require.NoError(t, err)
					require.NoError(t, opened.Close())
					require.Equal(t, "design\n", string(data))
					out, err := client.SubmitFormResponse(ctx, record.Key.TenantId, record.Key.JobId, input.FormSubmission{RequestID: form.RequestID, SubmissionID: "accepted", Fields: map[string]any{"decision": "approved", "annotated": jobdb.NewArtifactFromBytes("annotation.md", []byte("annotation"))}}, actor)
					require.NoError(t, err)
					_, duplicateErr := client.SubmitFormResponse(ctx, record.Key.TenantId, record.Key.JobId, input.FormSubmission{RequestID: form.RequestID, SubmissionID: "accepted", Fields: map[string]any{"decision": "approved"}}, actor)
					require.ErrorIs(t, duplicateErr, input.ErrInputNotPending, "preserve existing no-pending duplicate behavior, not a new receipt guarantee")
					return out.Receipt
				case "structured":
					out, err := client.SubmitStructuredResponse(ctx, record.Key.TenantId, record.Key.JobId, input.StructuredSubmission{RequestID: form.RequestID, SubmissionID: "accepted", Response: map[string]any{"decision": "approved"}}, actor)
					require.NoError(t, err)
					return out.Receipt
				default:
					var answer any = "approved"
					require.NoError(t, client.SubmitResponse(ctx, record.Key.TenantId, record.Key.JobId, input.FormResponse{Response: &answer}))
					return nil
				}
			}
			result := func() map[string]any {
				data, err := ctl.JobResult(ctx, record.Key)
				require.NoError(t, err)
				out, err := readJobOutputAsMap(data)
				require.NoError(t, err)
				return out
			}
			if produce {
				if name != "queued" {
					outcome := run()
					require.Equal(t, jobworkflow.JobRunSuspended, outcome.Status, "%+v", outcome)
					form := readForm()
					record.Form, err = json.Marshal(form)
					require.NoError(t, err)
					details, err := client.GetDetails(ctx, record.Key.TenantId, record.Key.JobId)
					require.NoError(t, err)
					record.Details, err = json.Marshal(details.Form)
					require.NoError(t, err)
					if name == "continuation" || name == "completed" {
						record.Receipt = submit(form)
					}
					if name == "completed" {
						outcome = run()
						require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status, "%+v", outcome)
						record.Result = result()
					}
				}
				raw, err := json.MarshalIndent(record, "", "  ")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "observations.json"), raw, 0600))
				t.Logf("baseline %s: mode=%s; persisted db, artifacts, workspace, form/details/result observations", name, record.Mode)
				return
			}
			if name == "completed" {
				require.Equal(t, record.Result, result())
				_, err := client.GetForm(ctx, record.Key.TenantId, record.Key.JobId)
				require.ErrorIs(t, err, input.ErrInputNotPending)
				return
			}
			if name == "queued" || name == "continuation" {
				outcome := run()
				require.Equal(t, jobworkflow.JobRunSuspended, outcome.Status, "%+v", outcome)
			}
			form := readForm()
			if name != "queued" && name != "continuation" {
				raw, err := json.Marshal(form)
				require.NoError(t, err)
				require.JSONEq(t, string(record.Form), string(raw))
				details, err := client.GetDetails(ctx, record.Key.TenantId, record.Key.JobId)
				require.NoError(t, err)
				raw, err = json.Marshal(details.Form)
				require.NoError(t, err)
				require.JSONEq(t, string(record.Details), string(raw))
			}
			// Unknown additive fields would fail a strict old decoder; absence must be
			// preserved on all these unannotated forms even after the runtime upgrade.
			raw, err := json.Marshal(form)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "document_metadata")
			require.NotContains(t, string(raw), "overview_document_id")
			page, err := client.ListPendingInputsPage(ctx, record.Key.TenantId, input.PendingInputOptions{PageSize: 1})
			require.NoError(t, err)
			require.Len(t, page.Inputs, 1)
			require.Equal(t, form.RequestID, page.Inputs[0].RequestID)
			if name == "continuation" {
				var previous input.InputForm
				require.NoError(t, json.Unmarshal(record.Form, &previous))
				require.NotEqual(t, previous.RequestID, form.RequestID)
				_, err := client.SubmitFormResponse(ctx, record.Key.TenantId, record.Key.JobId, input.FormSubmission{RequestID: previous.RequestID, SubmissionID: "stale", Response: true}, input.Actor{ID: "reviewer", Kind: "human"})
				require.ErrorIs(t, err, input.ErrStaleRequest)
				_, err = client.SubmitFormResponse(ctx, record.Key.TenantId, record.Key.JobId, input.FormSubmission{RequestID: form.RequestID, SubmissionID: "second", Response: true}, input.Actor{ID: "reviewer", Kind: "human"})
				require.NoError(t, err)
			} else if name != "fallback" && name != "timeout" {
				submit(form)
			}
			if name == "timeout" {
				// Ensure the pre-upgrade timeout is eligible without extending its deadline.
				time.Sleep(1100 * time.Millisecond)
				outcome := run()
				require.Equal(t, jobworkflow.JobRunFailed, outcome.Status, "%+v", outcome)
				var timeout jobdb.TimeoutError
				require.ErrorAs(t, outcome.JobError, &timeout)
				_, err := client.SubmitFormResponse(ctx, record.Key.TenantId, record.Key.JobId, input.FormSubmission{RequestID: form.RequestID, SubmissionID: "late", Fields: map[string]any{"decision": "late"}}, input.Actor{ID: "reviewer", Kind: "human"})
				require.ErrorIs(t, err, input.ErrInputNotPending)
				return
			}
			outcome := run()
			require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status, "%+v", outcome)
			output := result()
			if name == "continuation" {
				receipt, err := json.Marshal(record.Receipt)
				require.NoError(t, err)
				got, err := json.Marshal(output["first_receipt"])
				require.NoError(t, err)
				require.JSONEq(t, string(receipt), string(got))
			}
			if record.Mode == "review" {
				require.Contains(t, fmt.Sprint(output["preserved"]), "preserved\nannotation")
			}
			if name == "fallback" {
				require.Equal(t, "fallback", output["answer"])
				receipt := output["receipt"].(map[string]any)
				require.Equal(t, form.RequestID, receipt["request_id"])
				require.Equal(t, "automation", receipt["actor"].(map[string]any)["kind"])
			} else {
				require.Equal(t, "approved", output["answer"])
			}
			t.Logf("candidate resumed baseline %s with original artifacts/workspace and no migration", name)
		})
	}
}
