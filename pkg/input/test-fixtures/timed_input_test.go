package test_fixtures_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/colony-2/c2j/pkg/story"
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

type timedInputFixture struct {
	key      jobdb.JobKey
	ctl      *workflow.SWFWorkflowControl
	rt       jobdb.WorkflowRuntime
	engine   jobworkflow.Engine
	resolver compiler.RecipeSourceResolver
	run      func() jobworkflow.JobRunOutcome
	reopen   func()
}

func newTimedInputFixture(t *testing.T, source, scope string) *timedInputFixture {
	t.Helper()
	ensureFixtureOps()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recipe.yaml")
	require.NoError(t, os.WriteFile(path, []byte(source), 0600))
	repo, hash := createFixtureRepo(t, path, nil)
	rec, err := recipe.LoadRecipeFromString([]byte(source))
	require.NoError(t, err)
	f := &timedInputFixture{ctl: &workflow.SWFWorkflowControl{}}
	f.ctl.Registry, err = buildRecipeRegistry(path, rec, nil, repo, hash)
	require.NoError(t, err)
	f.resolver = compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{RecipeRefResolver: compiler.NewProviderBackedRecipeRefResolver(func(tenant, ref string) (*recipe.Recipe, error) { return f.ctl.Registry(tenant, ref) })})
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	// Close and recreate the HTTP service, database, engine, and worker set.
	// The only state shared between invocations is the persisted SQLite database.
	var closeStore func()
	var workers *jobworkflow.WorkSet
	open := func() {
		db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: dbPath})
		require.NoError(t, err)
		server := httptest.NewServer(remote.NewServer(db))
		closeStore = func() { server.Close(); require.NoError(t, db.Close(ctx)) }
		rt, err := remote.New(server.URL, server.Client())
		require.NoError(t, err)
		f.rt = rt
		engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
		require.NoError(t, err)
		f.engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}
		f.ctl.Engine = f.engine
		registry, err := workerops.NewActivityRegistry()
		require.NoError(t, err)
		deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(f.ctl).Build()
		workers, err = compiler.NewRecipeWorkerWithOptions(deps, registry, compiler.RecipeJobWorkerOptions{RootSourceResolver: f.resolver})
		require.NoError(t, err)
	}
	open()
	t.Cleanup(func() { closeStore() })
	f.reopen = func() { closeStore(); open() }
	jobCtx, gitCtx := generateTestContext(repo, hash, nil, nil)
	var embedded []recipe.Recipe
	if scope != "runtime-root" {
		embedded = []recipe.Recipe{*rec}
	}
	f.key, err = starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "timed-input", RecipeName: rec.GetMetadata().ID, JobContext: jobCtx, GitRef: gitCtx.ParentRef}, f.engine, embedded...)
	require.NoError(t, err)
	f.run = func() jobworkflow.JobRunOutcome {
		var tasks []jobworkflow.TaskWorker
		for _, task := range workers.TaskWorkers {
			tasks = append(tasks, task)
		}
		runnable, err := jobworkflow.GetJobForRun(ctx, f.rt, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: workers.JobWorker, TaskWorkers: tasks, WorkerID: "timed-input-worker", LeaseDuration: time.Minute})
		require.NoError(t, err)
		out, err := runnable.Run(nil)
		require.NoError(t, err)
		return out
	}
	return f
}

func timedInputSource(scope, mode, timeout string, documents bool) string {
	rootTimeout, opTimeout := "", ""
	if scope == "preloaded-root" || scope == "runtime-root" {
		rootTimeout = "timeout: " + timeout + "\n"
	}
	if scope == "input-op" {
		opTimeout = "    timeout: " + timeout + "\n"
	}
	form := "        question: Continue?\n        type: short_answer\n"
	answer := "response"
	if mode == "structured" {
		form = "        response_schema:\n          type: object\n          required: [decision]\n          properties:\n            decision: {type: string}\n"
		answer = "response.decision"
	}
	if mode == "review" {
		form = `        kind: review
        documents:
          design: '${{ sequence.prepare.artifacts["design.md"] }}'
        fields:
          - {id: decision, type: short_answer, question: 'Continue?', required: true}
          - {id: annotated, type: file_upload, question: Annotation}
`
		answer = "fields.decision"
	}
	prepare, after, outputs := "", "", ""
	if documents {
		prepare = `  - id: prepare
    op: command_execution
    inputs:
      run: 'echo preserved > proof.txt; echo design > "${{ context.environment.op.outbox }}/design.md"'
`
		after = `  - id: after
    op: command_execution
    inputs:
      run: cat proof.txt
`
		if mode == "review" {
			after = `  - id: after
    op: command_execution
    artifacts:
      annotated.md: '${{ sequence.question.outputs.fields.annotated }}'
    inputs:
      run: 'cat proof.txt; cat "${{ context.environment.op.inbox }}/annotated.md"'
`
		}
		outputs = "  preserved: '${{ sequence.after.outputs.stdout }}'\n"
	}
	return fmt.Sprintf("id: timed-input\nversion: \"1.0.0\"\n%ssequence:\n%s  - id: question\n%s    op: input\n    inputs:\n      form:\n%s%soutputs:\n  answer: '${{ sequence.question.outputs.%s }}'\n%s", rootTimeout, prepare, opTimeout, form, after, answer, outputs)
}

func TestTimedInputPublicAPIs(t *testing.T) {
	for _, scope := range []string{"untimed", "preloaded-root", "runtime-root", "input-op"} {
		for _, mode := range []string{"ordinary", "structured", "review"} {
			t.Run(scope+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				f := newTimedInputFixture(t, timedInputSource(scope, mode, "30s", true), scope)
				require.Equal(t, jobworkflow.JobRunSuspended, f.run().Status)
				before, err := f.rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: f.key})
				require.NoError(t, err)
				info, err := f.rt.GetJob(ctx, f.key)
				require.NoError(t, err)
				wait := info.ExecutionState.TaskWait
				require.NotNil(t, wait)
				// This deliberately verifies the v0.0.58 checkpoint shape, without a
				// new field or reordered chapter that would invalidate existing histories.
				if scope != "untimed" {
					rawHandle, err := f.engine.GetWaitingTask(ctx, f.key)
					require.NoError(t, err)
					rawData, err := rawHandle.Data()
					require.NoError(t, err)
					raw, err := rawData.GetData()
					require.NoError(t, err)
					var stamp map[string]any
					require.NoError(t, json.Unmarshal(raw, &stamp))
					require.Len(t, stamp, 1)
					require.Contains(t, stamp, "at")
				}
				client, err := input.NewRuntime(f.ctl, nil)
				require.NoError(t, err)
				form, err := client.GetForm(ctx, f.key.TenantId, f.key.JobId)
				require.NoError(t, err)
				page, err := client.ListPendingInputsPage(ctx, f.key.TenantId, input.PendingInputOptions{PageSize: 1})
				require.NoError(t, err)
				require.Len(t, page.Inputs, 1)
				require.Equal(t, form.RequestedAt, page.Inputs[0].RequestedAt)
				require.Equal(t, wait.OutputOrdinal, page.Inputs[0].TaskOrdinal)
				f.reopen()
				restored, err := client.GetForm(ctx, f.key.TenantId, f.key.JobId)
				require.NoError(t, err)
				require.Equal(t, form, restored)
				details, err := client.GetDetails(ctx, f.key.TenantId, f.key.JobId)
				require.NoError(t, err)
				require.NotNil(t, details.Form)
				afterReads, err := f.rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: f.key})
				require.NoError(t, err)
				require.Equal(t, before, afterReads)
				actor := input.Actor{ID: "reviewer", Kind: "human"}
				switch mode {
				case "ordinary":
					var answer any = "yes"
					require.NoError(t, client.SubmitResponse(ctx, f.key.TenantId, f.key.JobId, input.FormResponse{Response: &answer}))
				case "structured":
					_, err = client.SubmitStructuredResponse(ctx, f.key.TenantId, f.key.JobId, input.StructuredSubmission{RequestID: form.RequestID, SubmissionID: "answer", Response: map[string]any{"decision": "yes"}}, actor)
					require.NoError(t, err)
				case "review":
					doc, err := client.OpenReviewDocument(ctx, f.key.TenantId, f.key.JobId, form.RequestID, "design")
					require.NoError(t, err)
					content, err := io.ReadAll(doc)
					require.NoError(t, err)
					require.NoError(t, doc.Close())
					require.Equal(t, "design\n", string(content))
					_, err = client.SubmitFormResponse(ctx, f.key.TenantId, f.key.JobId, input.FormSubmission{RequestID: form.RequestID, SubmissionID: "answer", Fields: map[string]any{"decision": "yes", "annotated": jobdb.NewArtifactFromBytes("annotated.md", []byte("annotated\n"))}}, actor)
					require.NoError(t, err)
				}
				require.Equal(t, jobworkflow.JobRunCompleted, f.run().Status)
				output, err := f.ctl.JobResult(ctx, f.key)
				require.NoError(t, err)
				result, err := readJobOutputAsMap(output)
				require.NoError(t, err)
				require.Equal(t, "yes", result["answer"])
				preserved := "preserved"
				if mode == "review" {
					preserved += "\nannotated"
				}
				require.Equal(t, preserved, strings.TrimSpace(fmt.Sprint(result["preserved"])))
				afterCompletion, err := f.rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: f.key})
				require.NoError(t, err)
				require.Greater(t, len(afterCompletion), len(before))
				require.Equal(t, before, afterCompletion[:len(before)], "existing chapters must remain immutable on replay")
				assertTimedPublicOutcome(t, f, story.WorkflowStatusCompleted)
			})
		}
	}
}

func TestTimedInputExpiryPublicAPIs(t *testing.T) {
	for _, tc := range []struct{ scope, mode string }{
		{"preloaded-root", "ordinary"}, {"runtime-root", "ordinary"},
		{"input-op", "ordinary"}, {"input-op", "structured"}, {"input-op", "review"},
	} {
		t.Run(tc.scope+"/"+tc.mode, func(t *testing.T) {
			ctx := context.Background()
			f := newTimedInputFixture(t, timedInputSource(tc.scope, tc.mode, "3s", tc.mode == "review"), tc.scope)
			require.Equal(t, jobworkflow.JobRunSuspended, f.run().Status)
			client, err := input.NewRuntime(f.ctl, nil)
			require.NoError(t, err)
			form, err := client.GetForm(ctx, f.key.TenantId, f.key.JobId)
			require.NoError(t, err)
			f.reopen()
			time.Sleep(3200 * time.Millisecond)
			out := f.run()
			require.Equal(t, jobworkflow.JobRunFailed, out.Status)
			var timeout jobdb.TimeoutError
			require.ErrorAs(t, out.JobError, &timeout)
			page, err := client.ListPendingInputsPage(ctx, f.key.TenantId, input.PendingInputOptions{})
			require.NoError(t, err)
			require.Empty(t, page.Inputs)
			var answer any = "late"
			actor := input.Actor{ID: "reviewer", Kind: "human"}
			switch tc.mode {
			case "ordinary":
				err = client.SubmitResponse(ctx, f.key.TenantId, f.key.JobId, input.FormResponse{Response: &answer})
			case "structured":
				_, err = client.SubmitStructuredResponse(ctx, f.key.TenantId, f.key.JobId, input.StructuredSubmission{RequestID: form.RequestID, SubmissionID: "late", Response: map[string]any{"decision": "late"}}, actor)
			case "review":
				_, err = client.SubmitFormResponse(ctx, f.key.TenantId, f.key.JobId, input.FormSubmission{RequestID: form.RequestID, SubmissionID: "late", Fields: map[string]any{"decision": "late"}}, actor)
			}
			require.ErrorIs(t, err, input.ErrInputNotPending)
			assertTimedPublicOutcome(t, f, story.WorkflowStatusFailed)
		})
	}
}

func assertTimedPublicOutcome(t *testing.T, f *timedInputFixture, want story.WorkflowStatus) {
	t.Helper()
	service, err := story.New(story.ServiceConfig{Engine: f.engine, SchemaRegistry: f.rt.(jobdb.JobSchemaRegistry), RootSourceResolver: f.resolver})
	require.NoError(t, err)
	out, err := service.GetWorkflowOutcome(context.Background(), story.GetWorkflowOutcomeRequest{ProjectID: f.key.TenantId, JobID: f.key.JobId})
	require.NoError(t, err)
	require.Equal(t, want, out.Status)
	if want == story.WorkflowStatusFailed {
		require.NotNil(t, out.Error)
		require.Contains(t, *out.Error, "timed out")
	} else {
		require.Nil(t, out.Error)
	}
}

func TestTimedInputFallbackAfterRecovery(t *testing.T) {
	for _, scope := range []string{"preloaded-root", "runtime-root", "input-op"} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			source := timedInputSource(scope, "ordinary", "30s", true)
			source = strings.Replace(source, "        type: short_answer\n", "        type: short_answer\n      if_unanswered:\n        after: 600ms\n        response: fallback\n", 1)
			f := newTimedInputFixture(t, source, scope)
			require.Equal(t, jobworkflow.JobRunSuspended, f.run().Status)
			client, err := input.NewRuntime(f.ctl, nil)
			require.NoError(t, err)
			form, err := client.GetForm(ctx, f.key.TenantId, f.key.JobId)
			require.NoError(t, err)
			at, err := time.Parse(time.RFC3339Nano, form.FallbackAt)
			require.NoError(t, err)
			f.reopen()
			frozen, err := client.GetForm(ctx, f.key.TenantId, f.key.JobId)
			require.NoError(t, err)
			require.Equal(t, form, frozen)
			if delay := time.Until(at.Add(50 * time.Millisecond)); delay > 0 {
				time.Sleep(delay)
			}
			require.Equal(t, jobworkflow.JobRunCompleted, f.run().Status)
			out, err := f.ctl.JobResult(ctx, f.key)
			require.NoError(t, err)
			result, err := readJobOutputAsMap(out)
			require.NoError(t, err)
			require.Equal(t, "fallback", result["answer"])
			require.Equal(t, "preserved", strings.TrimSpace(fmt.Sprint(result["preserved"])))
		})
	}
}

func TestTimedInputAnswerBeforeExpiryWorkerWins(t *testing.T) {
	// Deadline passage alone must not introduce a new API rejection rule. The
	// human can complete the task until an expiry worker wins atomic completion.
	// Use an op scope: a separate enclosing job deadline can still fail the job.
	ctx := context.Background()
	f := newTimedInputFixture(t, timedInputSource("input-op", "ordinary", "3s", false), "input-op")
	require.Equal(t, jobworkflow.JobRunSuspended, f.run().Status)
	client, err := input.NewRuntime(f.ctl, nil)
	require.NoError(t, err)
	_, err = client.GetForm(ctx, f.key.TenantId, f.key.JobId)
	require.NoError(t, err)
	f.reopen()
	time.Sleep(3200 * time.Millisecond)
	var answer any = "human"
	require.NoError(t, client.SubmitResponse(ctx, f.key.TenantId, f.key.JobId, input.FormResponse{Response: &answer}))
	require.Equal(t, jobworkflow.JobRunCompleted, f.run().Status)
	out, err := f.ctl.JobResult(ctx, f.key)
	require.NoError(t, err)
	result, err := readJobOutputAsMap(out)
	require.NoError(t, err)
	require.Equal(t, "human", result["answer"])
	assertTimedPublicOutcome(t, f, story.WorkflowStatusCompleted)
}
