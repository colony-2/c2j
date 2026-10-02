package input

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	c2workflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	workflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type clientControl struct {
	workflowctl.WorkflowControl
	list     func(jobdb.ListJobsRequest) ([]workflowctl.JobItem, string, error)
	waiting  func(jobdb.JobKey) (workflowctl.TaskHandle, error)
	artifact func(string, jobdb.ArtifactKey) jobdb.Artifact
}

func (c *clientControl) ListJobs(_ context.Context, req jobdb.ListJobsRequest) ([]workflowctl.JobItem, string, error) {
	return c.list(req)
}
func (c *clientControl) GetWaitingTask(_ context.Context, key jobdb.JobKey) (workflowctl.TaskHandle, error) {
	return c.waiting(key)
}
func (c *clientControl) GetArtifactLazy(_ context.Context, tenant string, key jobdb.ArtifactKey) jobdb.Artifact {
	return c.artifact(tenant, key)
}

type clientTask struct {
	workflowctl.TaskHandle
	key     jobdb.JobKey
	ordinal int64
	data    jobdb.TaskData
	finish  func(jobdb.TaskData) error
}

func (t *clientTask) JobKey() jobdb.JobKey          { return t.key }
func (t *clientTask) TaskType() string              { return "input:collect_user_input" }
func (t *clientTask) TaskOrdinalToComplete() int64  { return t.ordinal }
func (t *clientTask) Data() (jobdb.TaskData, error) { return t.data, nil }
func (t *clientTask) Finish(_ context.Context, data jobdb.TaskData) error {
	if t.finish != nil {
		return t.finish(data)
	}
	return nil
}
func apiTask(t *testing.T, id string, ordinal int64, form InputForm) *clientTask {
	t.Helper()
	value, err := jsonValue(form)
	require.NoError(t, err)
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{OpOutput: value.(map[string]any)})
	require.NoError(t, err)
	return &clientTask{key: jobdb.JobKey{TenantId: "tenant", JobId: id}, ordinal: ordinal, data: jobdb.NewTaskDataOrPanic(env)}
}
func taskListing(task *clientTask) workflowctl.JobItem {
	return workflowctl.JobItem{JobSummary: jobdb.JobSummary{JobKey: task.key, ExecutionState: jobdb.ExecutionState{TaskWait: &jobdb.TaskWait{OutputOrdinal: task.ordinal}}}}
}

func TestPendingInputPagesAndLegacyListing(t *testing.T) {
	ctx := context.Background()
	forms := []InputForm{
		{Question: "legacy"},
		{Kind: "review", RequestID: "review-1", RequestedAt: "2026-09-30T01:02:03Z"},
		{RequestID: "structured-1", RequestedAt: "2026-09-30T01:03:03Z", ResponseSchema: map[string]any{}},
	}
	tasks := make([]*clientTask, len(forms))
	for i, form := range forms {
		tasks[i] = apiTask(t, fmt.Sprint(i), int64(i+1), form)
	}
	control := &clientControl{
		list: func(req jobdb.ListJobsRequest) ([]workflowctl.JobItem, string, error) {
			require.Equal(t, []string{"tenant"}, req.TenantIds)
			require.Equal(t, "input:collect_user_input", req.JobTasks[0].TaskType)
			if req.PageToken == "" {
				return []workflowctl.JobItem{taskListing(tasks[0]), taskListing(tasks[1])}, "page-2", nil
			}
			require.Equal(t, "page-2", req.PageToken)
			return []workflowctl.JobItem{taskListing(tasks[2])}, "", nil
		},
		waiting: func(key jobdb.JobKey) (workflowctl.TaskHandle, error) {
			for _, task := range tasks {
				if task.key == key {
					return task, nil
				}
			}
			return nil, jobdb.ErrJobNotFound
		},
	}
	runtime, err := NewRuntime(control, nil)
	require.NoError(t, err)
	page, err := runtime.ListPendingInputsPage(ctx, "tenant", PendingInputOptions{PageSize: 2})
	require.NoError(t, err)
	require.Len(t, page.Inputs, 2)
	require.Empty(t, page.Inputs[0].RequestedAt)
	require.Empty(t, page.Inputs[0].RequestID)
	require.Equal(t, forms[1].RequestedAt, page.Inputs[1].RequestedAt)
	require.Equal(t, int64(2), page.Inputs[1].TaskOrdinal)
	next, err := runtime.ListPendingInputsPage(ctx, "tenant", PendingInputOptions{PageSize: 2, PageToken: page.NextPageToken})
	require.NoError(t, err)
	require.Len(t, next.Inputs, 1)
	require.Empty(t, next.NextPageToken)
	legacy, err := runtime.ListPendingInputs(ctx, "tenant")
	require.NoError(t, err)
	require.Len(t, legacy, 3)
	_, err = runtime.ListPendingInputsPage(ctx, "tenant", PendingInputOptions{PageSize: -1})
	require.ErrorIs(t, err, ErrValidation)
}

func TestPendingInputPageConcurrentCompletionAndStorageError(t *testing.T) {
	old := apiTask(t, "job", 5, InputForm{Kind: "review", RequestID: "old"})
	newer := apiTask(t, "job", 8, InputForm{Kind: "review", RequestID: "new"})
	control := &clientControl{list: func(jobdb.ListJobsRequest) ([]workflowctl.JobItem, string, error) {
		return []workflowctl.JobItem{taskListing(old)}, "continue", nil
	}}
	runtime, _ := NewRuntime(control, nil)
	for _, wait := range []func(jobdb.JobKey) (workflowctl.TaskHandle, error){
		func(jobdb.JobKey) (workflowctl.TaskHandle, error) { return nil, jobdb.ErrJobNotFound },
		func(jobdb.JobKey) (workflowctl.TaskHandle, error) { return newer, nil },
	} {
		control.waiting = wait
		page, err := runtime.ListPendingInputsPage(context.Background(), "tenant", PendingInputOptions{})
		require.NoError(t, err)
		require.Empty(t, page.Inputs)
		require.Equal(t, "continue", page.NextPageToken)
	}
	unavailable := errors.New("database offline")
	control.waiting = func(jobdb.JobKey) (workflowctl.TaskHandle, error) { return nil, unavailable }
	_, err := runtime.ListPendingInputsPage(context.Background(), "tenant", PendingInputOptions{})
	require.ErrorIs(t, err, ErrStorage)
	require.ErrorIs(t, err, unavailable)
}

func TestReviewDocumentAccessAndOccurrenceLifetime(t *testing.T) {
	ctx := context.Background()
	ref := artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "child", TaskOrdinal: 7, Name: "design.md", SizeBytes: 4})
	other := artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "another-child", TaskOrdinal: 9, Name: "rollout.md", SizeBytes: 4})
	form := InputForm{Kind: "review", RequestID: "first", Documents: map[string]artifacts.Ref{"design": ref, "rollout": other}}
	current := apiTask(t, "job", 11, form)
	reads := 0
	control := &clientControl{
		waiting: func(key jobdb.JobKey) (workflowctl.TaskHandle, error) {
			if current == nil || key != current.key {
				return nil, jobdb.ErrJobNotFound
			}
			return current, nil
		},
		artifact: func(tenant string, key jobdb.ArtifactKey) jobdb.Artifact {
			require.Equal(t, "tenant", tenant)
			require.Contains(t, []string{"child", "another-child"}, key.JobId)
			reads++
			return jobdb.NewArtifactFromBytes(key.Name, []byte("body"))
		},
	}
	runtime, _ := NewRuntime(control, nil)
	for _, id := range []string{"design", "rollout"} {
		doc, err := runtime.OpenReviewDocument(ctx, "tenant", "job", "first", id)
		require.NoError(t, err)
		require.Equal(t, form.Documents[id], doc.Ref)
		require.Equal(t, int64(4), doc.SizeBytes)
		content, err := io.ReadAll(doc)
		require.NoError(t, err)
		require.Equal(t, "body", string(content))
		require.NoError(t, doc.Close())
	}
	_, err := runtime.OpenReviewDocument(ctx, "tenant", "job", "wrong", "design")
	require.ErrorIs(t, err, ErrStaleRequest)
	_, err = runtime.OpenReviewDocument(ctx, "tenant", "job", "first", "unknown")
	require.ErrorIs(t, err, ErrDocumentNotFound)
	var typed *RuntimeError
	require.ErrorAs(t, err, &typed)
	require.Equal(t, "unknown", typed.DocumentID)
	_, err = runtime.OpenReviewDocument(ctx, "other-tenant", "job", "first", "design")
	require.ErrorIs(t, err, ErrInputNotPending)
	require.Equal(t, 2, reads, "invalid requests must not read artifacts")
	doc, err := runtime.OpenReviewDocument(ctx, "tenant", "job", "first", "design")
	require.NoError(t, err)
	form.RequestID = "second"
	current = apiTask(t, "job", 14, form)
	_, err = runtime.OpenReviewDocument(ctx, "tenant", "job", "first", "design")
	require.ErrorIs(t, err, ErrStaleRequest)
	content, err := io.ReadAll(doc)
	require.NoError(t, err)
	require.Equal(t, "body", string(content))
	require.NoError(t, doc.Close())
	control.artifact = func(string, jobdb.ArtifactKey) jobdb.Artifact { return nil }
	_, err = runtime.OpenReviewDocument(ctx, "tenant", "job", "second", "design")
	require.ErrorIs(t, err, ErrArtifactUnavailable)
	for _, failOnOpen := range []bool{true, false} {
		control.artifact = func(string, jobdb.ArtifactKey) jobdb.Artifact {
			return jobdb.NewArtifact("design.md", func() (io.ReadCloser, int64, error) {
				if failOnOpen {
					return nil, 0, io.ErrUnexpectedEOF
				}
				return io.NopCloser(failedDocumentReader{}), 4, nil
			}, nil)
		}
		stream, err := runtime.OpenReviewDocument(ctx, "tenant", "job", "second", "design")
		if !failOnOpen {
			require.NoError(t, err)
			_, err = io.ReadAll(stream)
			require.NoError(t, stream.Close())
		}
		require.ErrorIs(t, err, ErrArtifactUnavailable)
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	}
	current = apiTask(t, "job", 17, InputForm{Question: "ordinary"})
	_, err = runtime.OpenReviewDocument(ctx, "tenant", "job", "second", "design")
	require.ErrorIs(t, err, ErrStaleRequest)
	current = nil
	_, err = runtime.OpenReviewDocument(ctx, "tenant", "job", "second", "design")
	require.ErrorIs(t, err, ErrInputNotPending)
}

func TestReviewSubmissionTypedErrors(t *testing.T) {
	form, err := buildForm(ops.NewOpDependenciesBuilder().Build(), context.Background(), Input{Form: reviewConfig()})
	require.NoError(t, err)
	task := apiTask(t, "job", 4, form)
	control := &clientControl{waiting: func(jobdb.JobKey) (workflowctl.TaskHandle, error) { return task, nil }, artifact: func(string, jobdb.ArtifactKey) jobdb.Artifact { return nil }}
	runtime, _ := NewRuntime(control, nil)
	actor := Actor{ID: "reviewer", Kind: "human"}
	for _, fields := range []map[string]any{{}, {"decision": "invalid"}} {
		_, err := runtime.SubmitFormResponse(context.Background(), "tenant", "job", FormSubmission{RequestID: form.RequestID, SubmissionID: "submission", Fields: fields}, actor)
		require.ErrorIs(t, err, ErrValidation)
		var validation ValidationError
		require.ErrorAs(t, err, &validation)
		require.Equal(t, "decision", validation.FieldID)
	}
	sub := FormSubmission{RequestID: "old", SubmissionID: "submission", Fields: map[string]any{"decision": "approve"}}
	_, err = runtime.SubmitFormResponse(context.Background(), "tenant", "job", sub, actor)
	require.ErrorIs(t, err, ErrStaleRequest)
	sub.RequestID = form.RequestID
	sub.Fields["document"] = artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "missing", TaskOrdinal: 2, Name: "file.md", SizeBytes: 4})
	_, err = runtime.SubmitFormResponse(context.Background(), "tenant", "job", sub, actor)
	require.ErrorIs(t, err, ErrArtifactUnavailable)
	var access *RuntimeError
	require.ErrorAs(t, err, &access)
	require.Equal(t, "document", access.FieldID)
	delete(sub.Fields, "document")
	failure := errors.New("disk full")
	task.finish = func(jobdb.TaskData) error { return failure }
	_, err = runtime.SubmitFormResponse(context.Background(), "tenant", "job", sub, actor)
	require.ErrorIs(t, err, ErrStorage)
	require.ErrorIs(t, err, failure)
	task.finish = func(jobdb.TaskData) error { return jobdb.ErrConflict }
	_, err = runtime.SubmitFormResponse(context.Background(), "tenant", "job", sub, actor)
	require.ErrorIs(t, err, ErrStorage)
	// A conflict after a competing completion has a different classification.
	calls := 0
	control.waiting = func(jobdb.JobKey) (workflowctl.TaskHandle, error) {
		calls++
		if calls == 1 {
			return task, nil
		}
		return nil, jobdb.ErrJobNotFound
	}
	_, err = runtime.SubmitFormResponse(context.Background(), "tenant", "job", sub, actor)
	require.ErrorIs(t, err, ErrInputNotPending)
	require.ErrorIs(t, err, jobdb.ErrConflict)
}

func TestFormRequestedTimeIsPersistedForEveryMode(t *testing.T) {
	for _, cfg := range []Config{{Question: "ordinary", Type: FieldTypeShortAnswer}, reviewConfig(), structuredConfig()} {
		raw, err := jsonValue(Input{Form: cfg})
		require.NoError(t, err)
		output, err := GetOp().TaskChain()[0].Invoke(ops.NewOpDependenciesBuilder().Build(), context.Background(), raw.(map[string]any))
		require.NoError(t, err)
		var form InputForm
		require.NoError(t, ops.DecodeWithJsonTags(output, &form))
		timestamp, err := time.Parse(time.RFC3339Nano, form.RequestedAt)
		require.NoError(t, err)
		require.WithinDuration(t, time.Now(), timestamp, time.Minute)
		if cfg.Kind == "" && cfg.ResponseSchema == nil {
			require.Empty(t, form.RequestID, "ordinary CLI behavior must stay compatible")
		}
	}
}

func TestPendingInputSQLitePaginationBeyondBackendPageLimit(t *testing.T) {
	ctx := context.Background()
	db, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: t.TempDir() + "/jobs.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close(ctx) })
	engine, err := workflow.NewEngineBuilder().WithRuntime(db).BuildEngine()
	require.NoError(t, err)
	runtime, err := NewRuntime(&c2workflow.SWFWorkflowControl{Engine: engine}, nil)
	require.NoError(t, err)
	const count = 203
	for i := 0; i < count; i++ {
		form, err := buildForm(ops.NewOpDependenciesBuilder().Build(), ctx, Input{Form: Config{Question: "ordinary", Type: FieldTypeShortAnswer}})
		require.NoError(t, err)
		worker := structuredWorker{forms: []InputForm{form}}
		handle, err := db.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: fmt.Sprintf("job-%03d", i), JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(map[string]any{}), RunPolicy: jobdb.DefaultRunPolicy()}})
		require.NoError(t, err)
		runnable, err := workflow.GetJobForRun(ctx, db, workflow.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: worker, TaskWorkers: []workflow.TaskWorker{structuredPrepareTask{}}, WorkerID: "test", LeaseDuration: time.Minute})
		require.NoError(t, err)
		_, err = runnable.Run(nil)
		require.NoError(t, err)
	}
	all, err := runtime.ListPendingInputs(ctx, "tenant")
	require.NoError(t, err)
	require.Len(t, all, count)
	seen := map[string]bool{}
	token := ""
	for {
		page, err := runtime.ListPendingInputsPage(ctx, "tenant", PendingInputOptions{PageSize: 75, PageToken: token})
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Inputs), 75)
		for _, item := range page.Inputs {
			require.False(t, seen[item.JobID])
			seen[item.JobID] = true
			require.NotEmpty(t, item.RequestedAt)
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}
	require.Len(t, seen, count)
}

type failedDocumentReader struct{}

func (failedDocumentReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
