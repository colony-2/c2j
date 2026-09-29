package test_fixtures_test

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/colony-2/c2j/pkg/ops/extensions"
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

func TestStructuredReviewRecipeWithAttachmentsAndWorkspace(t *testing.T) {
	ensureFixtureOps()
	coreops.Register(extensions.GetExecutionOp())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	primary := "../../../examples/review/review.yaml"
	repo, _ := createFixtureRepo(t, primary, nil)
	betaRepo, betaHash := createFixtureRepo(t, primary, nil)
	for _, name := range []string{"op.yaml", "prepare.py"} {
		data, err := os.ReadFile(filepath.Join("../../../extensions/review", name))
		require.NoError(t, err)
		dest := filepath.Join(repo, "extensions/review", name)
		require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0755))
		require.NoError(t, os.WriteFile(dest, data, 0644))
	}
	yaml, err := os.ReadFile(primary)
	require.NoError(t, err)
	// The review input runs in a fresh cell workspace while preparation stays in
	// the parent workspace. Completing the external task must preserve that scope.
	yaml = []byte(strings.Replace(string(yaml), "  - id: collect\n", fmt.Sprintf("  - id: collect\n    workspace: {cell: %q, ref: %q}\n", betaRepo, betaHash), 1))
	require.NoError(t, os.WriteFile(filepath.Join(repo, compiler.CellRecipeDirectory, "review.yaml"), yaml, 0644))
	require.NoError(t, runFixtureGit(repo, "add", "."))
	require.NoError(t, runFixtureGit(repo, "commit", "-m", "add review extension"))
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
	tasks := make([]jobworkflow.TaskWorker, 0, len(workers.TaskWorkers))
	for _, w := range workers.TaskWorkers {
		tasks = append(tasks, w)
	}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(embedded.Runtime).WithWorkerTenantId("test-tenant").WithLogger(slog.New(slog.NewTextHandler(os.Stderr, nil))).PlusWorkers(workers.JobWorker, tasks...).BuildEngine()
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
	spec := `{"title":"Design review","decisions":{"approve":{"label":"Approve","accepts_reviewed_content":true},"revise":{"label":"Revise","feedback_required":true}}}`
	jobKey, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "test-tenant", RecipeName: rec.GetMetadata().ID, Inputs: map[string]any{"spec_json": spec, "documents": map[string]any{"design": ref}}, JobContext: jobCtx, GitRef: gitCtx.ParentRef}, engine, *rec)
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

	doc := form.Request.(map[string]any)["documents"].(map[string]any)["design"].(map[string]any)
	actor := input.Actor{ID: "reviewer", Kind: "human"}
	_, err = runtime.SubmitStructuredResponse(ctx, jobKey.TenantId, jobKey.JobId, input.StructuredSubmission{RequestID: form.RequestID, SubmissionID: "invalid", Response: map[string]any{"decision": "revise"}}, actor)
	require.Error(t, err)
	annotation := []byte("# Design\n{++Explain retry behavior.++}\n")
	out, err := runtime.SubmitStructuredResponse(ctx, jobKey.TenantId, jobKey.JobId, input.StructuredSubmission{RequestID: form.RequestID, SubmissionID: "review-1", Response: map[string]any{"decision": "revise", "annotations": map[string]any{"design": map[string]any{"base_sha256": doc["sha256"], "format": "criticmarkup", "artifact": jobdb.NewArtifactFromBytes("design-annotated.md", annotation)}}}}, actor)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 30*time.Second, jobKey, engine))
	data, err := wf.JobResult(ctx, jobKey)
	require.NoError(t, err)
	result, err := readJobOutputAsMap(data)
	require.NoError(t, err)
	require.Equal(t, "revise", result["response"].(map[string]any)["decision"])
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
	require.Equal(t, "child-producer", doc["artifact"].(map[string]any)["stored"].(map[string]any)["key"].(map[string]any)["jobId"])
}
