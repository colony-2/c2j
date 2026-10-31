package test_fixtures_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
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
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewRecipeWithAttachmentsAndWorkspace(t *testing.T) {
	for _, annotated := range []bool{false, true} {
		t.Run(fmt.Sprintf("annotated=%v", annotated), func(t *testing.T) { testReviewRecipeWithAttachmentsAndWorkspace(t, annotated) })
	}
}

func testReviewRecipeWithAttachmentsAndWorkspace(t *testing.T, annotated bool) {
	ensureFixtureOps()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	primary := "../../../examples/review/review.yaml"
	repo, _ := createFixtureRepo(t, primary, nil)
	betaRepo, betaHash := createFixtureRepo(t, primary, nil)
	yaml, err := os.ReadFile(primary)
	require.NoError(t, err)
	if !annotated {
		start := strings.Index(string(yaml), "        # Requires a runtime")
		end := strings.Index(string(yaml), "        documents:")
		yaml = append(append([]byte{}, yaml[:start]...), yaml[end:]...)
	} else {
		yaml = []byte(strings.Replace(string(yaml), "            order: 0", `            order: '${{ jq(0, ".") }}'
            source_document_ids: [generated]`, 1))
	}
	// Completing the review in a fresh cell workspace must preserve that scope.
	yaml = []byte(strings.Replace(string(yaml), "  - id: review\n", fmt.Sprintf("  - id: review\n    workspace: {cell: %q, ref: %q}\n", betaRepo, betaHash), 1))
	// A generated review document must use the same independently readable
	// attachment API as the pre-existing stored document.
	yaml = []byte(strings.Replace(string(yaml), "        documents:\n", `        documents:
          generated:
            kind: value
            name: generated.txt
            format: text
            value: '${{ jq(0, "\"x\" * 524288") }}'
`, 1))
	// Consume the returned reference in an actual later op, after persistence.
	yaml = []byte(strings.Replace(string(yaml), "outputs:\n", `  - id: consume
    op: command_execution
    artifacts:
      returned.md: "${{ sequence.review.outputs.fields.annotated_design }}"
    inputs:
      run: 'cat "${{ context.environment.op.inbox }}/returned.md"'
outputs:
  consumed: "${{ sequence.consume.outputs.stdout }}"
`, 1))
	require.NoError(t, os.WriteFile(filepath.Join(repo, compiler.CellRecipeDirectory, "review.yaml"), yaml, 0644))
	require.NoError(t, runFixtureGit(repo, "add", "."))
	require.NoError(t, runFixtureGit(repo, "commit", "-m", "add review recipe"))
	hashBytes, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	hash := strings.TrimSpace(string(hashBytes))
	rec, err := recipe.LoadRecipeFromString(yaml)
	require.NoError(t, err)
	wf := &workflow.SWFWorkflowControl{PreferRuntimeRecipeResolution: true}
	wf.Registry, err = buildRecipeRegistry(primary, rec, nil, repo, hash)
	require.NoError(t, err)
	rootResolver := compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{
		RecipeRefResolver: compiler.NewProviderBackedRecipeRefResolver(func(projectID, ref string) (*recipe.Recipe, error) { return wf.Registry(projectID, ref) }),
	})
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(wf).Build()
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	workers, err := compiler.NewRecipeWorkerWithOptions(deps, registry, compiler.RecipeJobWorkerOptions{RootSourceResolver: rootResolver})
	require.NoError(t, err)
	embedded, err := sqliteruntime.StartEmbeddedRuntime(ctx)
	require.NoError(t, err)
	t.Cleanup(embedded.Shutdown)
	guard := executionruntime.New(embedded.Runtime, execution.Allocation{SchemaVersion: 1})
	tasks := make([]jobworkflow.TaskWorker, 0, len(workers.TaskWorkers))
	for _, w := range workers.TaskWorkers {
		tasks = append(tasks, guard.WrapTaskWorker(w))
	}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(guard).WithWorkerTenantId("test-tenant").WithLogger(slog.New(slog.NewTextHandler(os.Stderr, nil))).PlusWorkers(workers.JobWorker, tasks...).BuildEngine()
	require.NoError(t, err)
	engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: embedded.Runtime}
	wf.Engine = engine
	done := make(chan struct{})
	go func() { defer close(done); engine.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	// Store a document under a different producer job. Its key must survive review.
	original := jobdb.NewArtifactFromBytes("design.md", []byte("# Design\nRecovery is explicit.\n"))
	_, err = embedded.Runtime.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test-tenant", JobID: "child-producer", JobType: "artifact-source", Data: jobdb.NewTaskDataOrPanic(map[string]any{}, original), RunPolicy: jobdb.DefaultRunPolicy()}})
	require.NoError(t, err)
	key, err := original.ArtifactKey()
	require.NoError(t, err)
	ref := recipeartifacts.NewStoredRef(key)
	jobCtx, gitCtx := generateTestContext(repo, hash, nil, nil)
	jobKeyRef, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "test-tenant", RecipeName: rec.GetMetadata().ID, Inputs: map[string]any{"prompt": "Review the design"}, ArtifactRefs: []recipeartifacts.Ref{ref}, JobContext: jobCtx, GitRef: gitCtx.ParentRef}, engine, *rec)
	jobKey := jobKeyRef.Key()
	require.NoError(t, err)
	runtime, err := input.NewRuntime(wf, nil)
	require.NoError(t, err)
	var form input.InputForm
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		form, err = runtime.GetForm(ctx, jobKey.TenantId, jobKey.JobId)
		assert.NoError(c, err)
		inspection, inspectErr := wf.InspectJob(ctx, jobKey)
		assert.NoError(c, inspectErr)
		assert.False(c, inspection.Terminal, "job inspection: %+v", inspection)
	}, 15*time.Second, 100*time.Millisecond)

	require.Equal(t, "review", form.Kind)
	require.Nil(t, form.ResponseSchema)
	doc := form.Documents["design"]
	opened, err := runtime.OpenReviewDocument(ctx, jobKey.TenantId, jobKey.JobId, form.RequestID, "design")
	require.NoError(t, err)
	require.Equal(t, doc, opened.Ref)
	originalBytes, err := io.ReadAll(opened)
	require.NoError(t, err)
	require.NoError(t, opened.Close())
	require.Equal(t, "# Design\nRecovery is explicit.\n", string(originalBytes))
	generated, err := runtime.OpenReviewDocument(ctx, jobKey.TenantId, jobKey.JobId, form.RequestID, "generated")
	require.NoError(t, err)
	generatedBytes, err := io.ReadAll(generated)
	require.NoError(t, err)
	require.NoError(t, generated.Close())
	require.Equal(t, strings.Repeat("x", 524288), string(generatedBytes))
	require.Equal(t, "generated.txt", generated.Ref.NameValue())
	details, err := runtime.GetDetails(ctx, jobKey.TenantId, jobKey.JobId)
	require.NoError(t, err)
	require.Equal(t, "review", *details.Form.Kind)
	require.Contains(t, *details.Form.Documents, "design")
	if annotated {
		require.Equal(t, "design", form.OverviewDocumentID)
		require.Equal(t, []string{"generated"}, form.DocumentMetadata["design"].SourceDocumentIDs)
		require.Zero(t, *form.DocumentMetadata["design"].Order)
		require.Equal(t, "design", *details.Form.OverviewDocumentId)
		require.Zero(t, *(*details.Form.DocumentMetadata)["design"].Order)
	} else {
		require.Empty(t, form.DocumentMetadata)
		require.Nil(t, details.Form.DocumentMetadata)
		require.Nil(t, details.Form.OverviewDocumentId)
	}
	actor := input.Actor{ID: "reviewer", Kind: "human"}
	_, err = runtime.SubmitFormResponse(ctx, jobKey.TenantId, jobKey.JobId, input.FormSubmission{RequestID: form.RequestID, SubmissionID: "invalid", Fields: map[string]any{"decision": "unknown"}}, actor)
	require.Error(t, err)
	annotation := []byte("# Design\nExplain retry behavior.\n")
	out, err := runtime.SubmitFormResponse(ctx, jobKey.TenantId, jobKey.JobId, input.FormSubmission{RequestID: form.RequestID, SubmissionID: "review-1", Fields: map[string]any{"decision": "revise", "annotated_design": jobdb.NewArtifactFromBytes("design-annotated.md", annotation)}}, actor)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 30*time.Second, jobKey, engine))
	data, err := wf.JobResult(ctx, jobKey)
	require.NoError(t, err)
	result, err := readJobOutputAsMap(data)
	require.NoError(t, err)
	require.Equal(t, "revise", result["answers"].(map[string]any)["decision"])
	require.Equal(t, strings.TrimSpace(string(annotation)), result["consumed"])
	receiptJSON, err := json.Marshal(out.Receipt)
	require.NoError(t, err)
	var receiptMap map[string]any
	require.NoError(t, json.Unmarshal(receiptJSON, &receiptMap))
	require.Equal(t, receiptMap, result["receipt"])
	annotationKey, ok := out.ArtifactRefs["design-annotated.md"].StoredKey()
	require.True(t, ok)
	bytes, err := wf.GetArtifactLazy(ctx, jobKey.TenantId, annotationKey).Bytes(ctx)
	require.NoError(t, err)
	require.Equal(t, annotation, bytes)
	require.Equal(t, "child-producer", doc.Stored.Key.JobId)
}

// Invalid evaluated annotations must fail before the review wait is published.
func TestReviewRecipeRejectsEvaluatedMetadata(t *testing.T) {
	for _, test := range []struct{ name, metadata string }{
		{"fractional", `order: '${{ jq(0, "0.5") }}'`},
		{"overflow", `order: '${{ jq(0, "1e30") }}'`},
		{"negative", `order: '${{ jq(0, "-1") }}'`},
		{"role", `role: '${{ jq(0, "\"invalid\"") }}'`},
		{"source", `source_document_ids: [missing]`},
		{"title", `title: '${{ jq(0, "\"x\" * 121") }}'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := timedInputSource("none", "review", "", true)
			source = strings.Replace(source, "        documents:\n", "        document_metadata:\n          design:\n            "+test.metadata+"\n        documents:\n", 1)
			fixture := newTimedInputFixture(t, source, "none")
			result := fixture.run()
			require.Equal(t, jobworkflow.JobRunFailed, result.Status, "%+v", result)
			client, err := input.NewRuntime(fixture.ctl, nil)
			require.NoError(t, err)
			page, err := client.ListPendingInputsPage(context.Background(), fixture.key.TenantId, input.PendingInputOptions{})
			require.NoError(t, err)
			require.Empty(t, page.Inputs)
		})
	}
}

func TestAnnotatedReviewCheckpointRecovery(t *testing.T) {
	source := timedInputSource("none", "review", "", true)
	source = strings.Replace(source, "        documents:", `        overview_document_id: design
        document_metadata:
          design:
            title: Frozen proposal
            role: primary
            order: 0
            source_document_ids: [source]
        documents:
          source: '${{ sequence.prepare.artifacts["design.md"] }}'`, 1)
	fixture := newTimedInputFixture(t, source, "none")
	require.Equal(t, jobworkflow.JobRunSuspended, fixture.run().Status)
	client, err := input.NewRuntime(fixture.ctl, nil)
	require.NoError(t, err)
	first, err := client.GetForm(context.Background(), fixture.key.TenantId, fixture.key.JobId)
	require.NoError(t, err)
	frozen, err := json.Marshal(first)
	require.NoError(t, err)
	// Mutating a returned form cannot edit the persisted generate_form outcome.
	metadata := first.DocumentMetadata["design"]
	*metadata.Order = 77
	metadata.SourceDocumentIDs[0] = "changed"
	first.DocumentMetadata["design"] = metadata
	fixture.reopen()
	restored, err := client.GetForm(context.Background(), fixture.key.TenantId, fixture.key.JobId)
	require.NoError(t, err)
	raw, err := json.Marshal(restored)
	require.NoError(t, err)
	require.JSONEq(t, string(frozen), string(raw))
	_, err = client.SubmitFormResponse(context.Background(), fixture.key.TenantId, fixture.key.JobId, input.FormSubmission{RequestID: restored.RequestID, SubmissionID: "recovered", Fields: map[string]any{"decision": "approved", "annotated": jobdb.NewArtifactFromBytes("annotation.md", []byte("annotation"))}}, input.Actor{ID: "reviewer", Kind: "human"})
	require.NoError(t, err)
	require.Equal(t, jobworkflow.JobRunCompleted, fixture.run().Status)
	result, err := fixture.ctl.JobResult(context.Background(), fixture.key)
	require.NoError(t, err)
	output, err := readJobOutputAsMap(result)
	require.NoError(t, err)
	require.Equal(t, "approved", output["answer"])
	require.Contains(t, output["preserved"], "preserved\nannotation")
}
