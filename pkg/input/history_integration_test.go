package input

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	c2workflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	workflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// This fixture crosses the real engine and durable SQLite chapter/artifact
// boundaries. It never uses the configured shared JobDB deployment.
func TestHistorySQLiteRetainedWaitSnapshotsAndProducerBytes(t *testing.T) {
	ctx := celltest.Context(context.Background())
	path := filepath.Join(t.TempDir(), "history.db")
	rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: path, MaxChapterApplicationPayloadBytes: 4 * 1024 * 1024})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close(ctx) })
	celltest.Create(t, rt, "tenant", "test", "example.test/test")
	original := jobdb.NewArtifactFromBytes("same.md", []byte("child original"))
	_, err = rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "child-producer", JobType: "source", Data: jobdb.NewTaskDataOrPanic(nil, original), Cell: "test", CellRepo: "example.test/test"}})
	require.NoError(t, err)
	producerKey, err := original.ArtifactKey()
	require.NoError(t, err)
	engine, err := workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	ctl := &c2workflow.SWFWorkflowControl{Engine: engine}
	runtime, err := NewRuntime(ctl, nil)
	require.NoError(t, err)
	deps := coreops.NewOpDependenciesBuilder().WithWorkflowControl(&clientControl{artifact: func(_ string, key jobdb.ArtifactKey) jobdb.Artifact { return ctl.GetArtifactLazy(ctx, "tenant", key) }}).Build()
	cfg := reviewConfig()
	cfg.Documents = map[string]any{"document": artifacts.NewStoredRef(producerKey)}
	first, err := buildForm(deps, ctx, Input{Form: cfg})
	require.NoError(t, err)
	ordinary := InputForm{Question: "ordinary", Type: FieldTypeBoolean, Default: false}
	// An escaped large input uses the actual private-payload writer and is
	// restored after closing and reopening SQLite.
	third, err := buildForm(deps, ctx, Input{Form: Config{Request: map[string]any{"prompt": strings.Repeat("<", 65536)}, ResponseSchema: map[string]any{}}})
	require.NoError(t, err)
	w := structuredWorker{forms: []InputForm{first, ordinary, third}}
	worker := w.executionWorker()
	execute := worker.Execute
	worker.Execute = func(c workflow.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
		out, e := execute(c, in)
		if e != nil {
			return out, e
		}
		return nil, fmt.Errorf("failure after accepted answer")
	}
	policy := jobdb.DefaultRunPolicy()
	policy.Retry.MaximumAttempts = 1
	handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "job", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(map[string]any{}), RunPolicy: policy, Cell: "test", CellRepo: "example.test/test"}})
	require.NoError(t, err)
	run := func() {
		r, e := workflow.GetJobForRun(ctx, rt, workflow.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: worker, TaskWorkers: []workflow.TaskWorker{structuredPrepareTask{}}, WorkerID: "history-test", LeaseDuration: time.Minute})
		require.NoError(t, e)
		_, e = r.Run(nil)
		require.NoError(t, e)
	}
	run()
	s, err := NewHistoryService(HistoryConfig{Reader: ctl})
	require.NoError(t, err)
	list := func(s HistoryService) *JobInputStoryPage {
		p, e := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "tenant", JobID: "job"})
		require.NoError(t, e)
		return p
	}
	p := list(s)
	require.Len(t, p.Entries, 1)
	firstID := p.Entries[0].EntryID
	d, err := s.GetJobInputStoryEntry(ctx, GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: firstID, SnapshotToken: p.SnapshotToken})
	require.NoError(t, err)
	require.True(t, d.Actionability.Actionable, "%+v", d.Actionability)
	// Save request membership before accepting an uploaded response attachment.
	require.Len(t, d.RequestManifest, 1)
	require.Empty(t, d.ResponseManifest)
	_, err = runtime.SubmitFormResponse(ctx, "tenant", "job", FormSubmission{RequestID: first.RequestID, SubmissionID: "first", Fields: map[string]any{"decision": "approve", "document": jobdb.NewArtifactFromBytes("same.md", []byte("response original"))}}, Actor{ID: "human", Kind: "human"})
	require.NoError(t, err)
	run()
	p2 := list(s)
	require.Len(t, p2.Entries, 2)
	require.Equal(t, firstID, p2.Entries[0].EntryID)
	require.Equal(t, "answered", p2.Entries[0].State)
	stale, err := s.GetJobInputStoryEntry(ctx, GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: firstID, SnapshotToken: p.SnapshotToken})
	require.NoError(t, err)
	require.Equal(t, "pending", stale.State)
	require.Empty(t, stale.ResponseManifest)
	require.False(t, stale.Actionability.Actionable)
	_, err = runtime.SubmitFormResponse(ctx, "tenant", "job", FormSubmission{RequestID: first.RequestID, SubmissionID: "stale", Fields: map[string]any{"decision": "approve"}}, Actor{ID: "human", Kind: "human"})
	require.Error(t, err)
	v := any(false)
	require.NoError(t, runtime.SubmitResponse(ctx, "tenant", "job", FormResponse{Response: &v}))
	run()
	two := list(s)
	require.Len(t, two.Entries, 3)
	require.Equal(t, "answered", two.Entries[0].State)
	require.Equal(t, "answered", two.Entries[1].State)
	require.Equal(t, "pending", two.Entries[2].State)
	// Reopen the backend while the private-input wait is retained.
	require.NoError(t, rt.Close(ctx))
	rt, err = sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: path, MaxChapterApplicationPayloadBytes: 4 * 1024 * 1024})
	require.NoError(t, err)
	engine, err = workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	ctl = &c2workflow.SWFWorkflowControl{Engine: engine}
	runtime, err = NewRuntime(ctl, nil)
	require.NoError(t, err)
	s, err = NewHistoryService(HistoryConfig{Reader: ctl})
	require.NoError(t, err)
	page, err := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "tenant", JobID: "job", PageSize: 1})
	require.NoError(t, err)
	pendingID := two.Entries[2].EntryID
	pending, err := s.GetJobInputStoryEntry(ctx, GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: pendingID, SnapshotToken: page.SnapshotToken})
	require.NoError(t, err)
	require.Equal(t, "structured", pending.Kind)
	var savedRequest struct {
		Prompt string `json:"prompt"`
	}
	require.NoError(t, json.Unmarshal(pending.FormFields["request"], &savedRequest))
	require.Equal(t, strings.Repeat("<", 65536), savedRequest.Prompt)
	require.True(t, pending.Actionability.Actionable, "%+v", pending.Actionability)
	// Barrier-controlled acceptance happens after the first page and before all
	// continuation reads. Every old page must still see a pending third entry.
	ready, done := make(chan struct{}), make(chan struct{})
	var submitErr error
	go func() {
		defer close(done)
		<-ready
		_, submitErr = runtime.SubmitStructuredResponse(ctx, "tenant", "job", StructuredSubmission{RequestID: third.RequestID, SubmissionID: "third", Response: json.Number("9007199254740993")}, Actor{ID: "human", Kind: "human"})
	}()
	close(ready)
	<-done
	require.NoError(t, submitErr)
	run()
	count := 1
	token := page.NextPageToken
	for token != "" {
		next, e := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "tenant", JobID: "job", PageSize: 1, PageToken: token})
		require.NoError(t, e)
		if next.Entries[0].EntryID == pendingID {
			require.Equal(t, "pending", next.Entries[0].State)
			require.False(t, next.Entries[0].AcceptedResponse.Present)
		}
		count++
		token = next.NextPageToken
	}
	require.Equal(t, 3, count)
	fresh := list(s)
	require.Equal(t, 3, len(fresh.Entries))
	require.Equal(t, pendingID, fresh.Entries[2].EntryID)
	require.Equal(t, "answered", fresh.Entries[2].State)
	saved, err := s.GetJobInputStoryEntry(ctx, GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: firstID})
	require.NoError(t, err)
	require.Len(t, saved.RequestManifest, 1)
	require.NotEmpty(t, saved.ResponseManifest)
	opened, err := s.OpenJobInputStoryArtifact(ctx, OpenJobInputStoryArtifactRequest{TenantID: "tenant", JobID: "job", EntryID: firstID, ArtifactID: saved.RequestManifest[0].ArtifactID, SnapshotToken: saved.SnapshotToken})
	require.NoError(t, err)
	b, err := io.ReadAll(opened)
	require.NoError(t, err)
	require.NoError(t, opened.Close())
	require.Equal(t, "child original", string(b))
	require.Equal(t, "child-producer", opened.Ref.Stored.Key.JobId)
	var responseMember HistoryArtifactMember
	for _, m := range saved.ResponseManifest {
		if m.Ref.NameValue() == "same.md" {
			responseMember = m
			break
		}
	}
	require.NotEmpty(t, responseMember.ArtifactID)
	opened, err = s.OpenJobInputStoryArtifact(ctx, OpenJobInputStoryArtifactRequest{TenantID: "tenant", JobID: "job", EntryID: firstID, ArtifactID: responseMember.ArtifactID, SnapshotToken: saved.SnapshotToken})
	require.NoError(t, err)
	b, err = io.ReadAll(opened)
	require.NoError(t, err)
	require.NoError(t, opened.Close())
	require.Equal(t, "response original", string(b))
	info, err := rt.GetJob(ctx, handle.JobKey)
	require.NoError(t, err)
	require.Equal(t, "failed_app", info.CompletionStatus)
	t.Logf("archived completion status: %s", info.CompletionStatus)
	archived, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, JobKeys: []jobdb.JobKey{handle.JobKey}, Stores: []jobdb.JobStore{jobdb.JobStoreArchived}})
	require.NoError(t, err)
	require.Len(t, archived.Jobs, 1, "fixture must actually be archived")
	_, err = s.OpenJobInputStoryArtifact(ctx, OpenJobInputStoryArtifactRequest{TenantID: "foreign", JobID: "job", EntryID: firstID, ArtifactID: saved.RequestManifest[0].ArtifactID})
	require.Error(t, err)
}

func TestHistorySQLiteExactRawAndForbiddenPaths(t *testing.T) {
	ctx := celltest.Context(context.Background())
	rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "raw.db"), MaxChapterApplicationPayloadBytes: 4 * 1024 * 1024})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close(ctx) })
	celltest.Create(t, rt, "tenant", "test", "example.test/test")
	engine, err := workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	ctl := &c2workflow.SWFWorkflowControl{Engine: engine}
	var fetches atomic.Int32
	remoteSchema := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		http.Error(w, "history must not fetch schemas", 500)
	}))
	t.Cleanup(remoteSchema.Close)
	input, err := workerops.NewInvocationTaskData(workerops.ActivityInvocationRequest{Input: map[string]any{"request_schema": map[string]any{"$ref": remoteSchema.URL}, "question": strings.Repeat("<", 65536), "default": json.Number("9007199254740993"), "explicit_null": nil}})
	require.NoError(t, err)
	worker := workflow.ExecutionWorker{JobType: "recipe", Execute: func(c workflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		return c.DoTask(workflow.TaskCall{Site: "raw", TaskType: "input:collect_user_input", ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return input, nil })
	}}
	policy := jobdb.DefaultRunPolicy()
	policy.Retry.MaximumAttempts = 1
	handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "job", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(nil), RunPolicy: policy, Cell: "test", CellRepo: "example.test/test"}})
	require.NoError(t, err)
	run := func() {
		r, e := workflow.GetJobForRun(ctx, rt, workflow.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: worker, WorkerID: "raw", LeaseDuration: time.Minute})
		require.NoError(t, e)
		_, e = r.Run(nil)
		require.NoError(t, e)
	}
	run()
	readEngine, err := workflow.NewEngineBuilder().WithRuntime(historyReadRuntime{WorkflowRuntime: rt}).BuildEngine()
	require.NoError(t, err)
	guarded := historyGuardReader{HistoryReader: &c2workflow.SWFWorkflowControl{Engine: readEngine}}
	s, err := NewHistoryService(HistoryConfig{Reader: guarded})
	require.NoError(t, err)
	list := func() *JobInputStoryPage {
		p, e := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "tenant", JobID: "job"})
		require.NoError(t, e)
		return p
	}
	before := list()
	require.Len(t, before.Entries, 1)
	id := before.Entries[0].EntryID
	require.Equal(t, "9007199254740993", string(before.Entries[0].FormFields["default"]))
	require.Equal(t, "null", string(before.Entries[0].FormFields["explicit_null"]))
	_, absent := before.Entries[0].FormFields["missing"]
	require.False(t, absent)
	wait, err := ctl.GetWaitingTask(ctx, handle.JobKey)
	require.NoError(t, err)
	// Raw normalized output fixture tests bytes that storage really retains,
	// independently of any older writer's number normalization.
	raw := `{"response":9007199254740993,"fields":{"false":false,"zero":0,"null":null,"empty":"","array":[],"object":{}}}`
	require.NoError(t, wait.Finish(ctx, &jobdb.SimpleTaskData{Data: historyOut(raw).Data}))
	run()
	after := list()
	require.Len(t, after.Entries, 1)
	require.Equal(t, id, after.Entries[0].EntryID)
	require.Equal(t, raw, string(after.Entries[0].AcceptedResponse.Raw))
	d, err := s.GetJobInputStoryEntry(ctx, GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: id})
	require.NoError(t, err)
	require.Empty(t, d.RequestManifest)
	require.Equal(t, "read_only", d.Actionability.Observation)
	old, err := s.GetJobInputStoryEntry(ctx, GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: id, SnapshotToken: before.SnapshotToken})
	require.NoError(t, err)
	require.False(t, old.AcceptedResponse.Present)
	require.Zero(t, fetches.Load(), "remote schema fetched during history reconstruction")
}

// Run the real compiler and input task workers synchronously to a durable wait.
// This checks production invocation IDs, not hand-authored execution records.
func TestHistorySQLiteCompilerLineage(t *testing.T) {
	ctx := celltest.Context(context.Background())
	rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "compiler.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rt.Close(ctx)) })
	jobCtx, gitCtx := compiler.GenerateTestContext()
	repo := celltest.Repo(jobCtx.GitBase.BaseRepo)
	celltest.Create(t, rt, "history", "test", repo)
	engine, err := workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	ctl := &c2workflow.SWFWorkflowControl{Engine: engine}
	coreops.Register(GetOp(), GetAutoFillOp(), GetAlternateOp())
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(ctl).WithSSEManager(NewSimpleSSEManager()).Build()
	work, err := compiler.NewRecipeWorker(deps, registry)
	require.NoError(t, err)
	tasks := make([]workflow.TaskWorker, 0, len(work.TaskWorkers))
	guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
	for _, task := range work.TaskWorkers {
		tasks = append(tasks, guard.WrapTaskWorker(task))
	}
	const rec = `id: history-lineage
version: "1.0"
input_schema: {}
sequence:
  - id: first
    op: input
    inputs:
      form:
        question: First
        type: boolean
        autofill: {response: false}
  - id: second
    op: input
    inputs:
      form:
        question: Second
        type: short_answer
        autofill: {response: accepted}
  - id: third
    op: input
    inputs:
      form:
        request: {prompt: Third}
        response_schema: {}
  - id: fourth
    op: input
    inputs:
      form:
        question: Fourth
        type: short_answer
outputs: {}
`
	data := jobdb.NewTaskDataOrPanic(workflowctl.StartJob{TenantId: "history", RecipeName: "history-lineage", JobContext: jobCtx, GitRef: gitCtx.ParentRef, Cell: "test", CellRepo: repo}, jobdb.NewArtifactFromBytes("history-lineage"+starter.RecipeArtifactSuffix, []byte(rec)))
	h, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "history", JobType: "recipe", Data: data, Cell: "test", CellRepo: repo})
	require.NoError(t, err)
	run := func() {
		r, e := workflow.GetJobForRun(ctx, guard, workflow.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: work.JobWorker, TaskWorkers: tasks, WorkerID: "history-compiler", LeaseDuration: time.Minute})
		require.NoError(t, e)
		_, e = r.Run(nil)
		require.NoError(t, e)
	}
	run()
	s, err := NewHistoryService(HistoryConfig{Reader: ctl})
	require.NoError(t, err)
	p, err := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "history", JobID: h.JobKey.JobId})
	require.NoError(t, err)
	if len(p.Entries) != 3 {
		stored, e := ctl.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: h.JobKey, IncludeInputs: true, IncludeOutputs: true})
		require.NoError(t, e)
		b, _ := json.MarshalIndent(stored, "", "  ")
		t.Log(string(b))
	}
	require.Len(t, p.Entries, 3, "%+v", p)
	require.Equal(t, "complete", p.Completeness, "%+v", p.Diagnostics)
	for i, entry := range p.Entries {
		require.NotEmpty(t, entry.Locator.Restart.ScopeID, "%+v", entry.Locator.Restart)
		require.Equal(t, HistoryRestartContaining, entry.Locator.Restart.Relationship)
		require.NotNil(t, entry.Locator.GenerationOrdinal, "%+v", entry)
		require.Len(t, entry.Locator.CollectionInvocationIDs, 1)
		if i < 2 {
			require.Equal(t, "answered", entry.State)
			require.Equal(t, "autofill", entry.ResponseSource)
		} else {
			require.Equal(t, "pending", entry.State)
		}
	}
	restartService, err := story.New(story.ServiceConfig{Engine: engine, SchemaRegistry: rt})
	require.NoError(t, err)
	selected := p.Entries[0].Locator.Restart
	restartRequest := story.RestartRecipeJobRequest{ProjectID: "history", JobID: h.JobKey.JobId, ScopeID: selected.ScopeID, RequestID: "history-restart"}
	conflictingOrdinal := int64(0)
	restartRequest.ChapterOrdinal = &conflictingOrdinal
	_, err = restartService.RestartRecipeJob(ctx, restartRequest)
	require.Error(t, err, "existing selector validation remains authoritative")
	restartRequest.ChapterOrdinal = nil
	restartResult, err := restartService.RestartRecipeJob(ctx, restartRequest)
	require.NoError(t, err)
	require.NotEqual(t, h.JobKey.JobId, restartResult.JobID)
	require.Equal(t, "history", restartResult.TenantID)
	// Restart is a separate explicit action. No worker runs the new job here.
	runtime, err := NewRuntime(ctl, nil)
	require.NoError(t, err)
	page, err := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "history", JobID: h.JobKey.JobId, PageSize: 1})
	require.NoError(t, err)
	detail, e := s.GetJobInputStoryEntry(ctx, GetJobInputStoryEntryRequest{TenantID: "history", JobID: h.JobKey.JobId, EntryID: p.Entries[2].EntryID, SnapshotToken: p.SnapshotToken})
	require.NoError(t, e)
	require.True(t, detail.Actionability.Actionable, "%+v", detail.Actionability)
	ready, done := make(chan struct{}), make(chan error, 1)
	answer := any("third accepted")
	go func() {
		<-ready
		_, e := runtime.SubmitStructuredResponse(ctx, "history", h.JobKey.JobId, StructuredSubmission{RequestID: p.Entries[2].RequestID, SubmissionID: "third", Response: answer}, Actor{ID: "human", Kind: "human"})
		done <- e
	}()
	close(ready)
	require.NoError(t, <-done)
	run()
	var oldIDs []string
	oldIDs = append(oldIDs, page.Entries[0].EntryID)
	token := page.NextPageToken
	for token != "" {
		next, e := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "history", JobID: h.JobKey.JobId, PageSize: 1, PageToken: token})
		require.NoError(t, e)
		oldIDs = append(oldIDs, next.Entries[0].EntryID)
		if len(oldIDs) == 3 {
			require.Equal(t, "pending", next.Entries[0].State)
		}
		token = next.NextPageToken
	}
	require.Len(t, oldIDs, 3)
	for i := range oldIDs {
		require.Equal(t, p.Entries[i].EntryID, oldIDs[i])
	}
	refreshed, e := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "history", JobID: h.JobKey.JobId})
	require.NoError(t, e)
	require.Len(t, refreshed.Entries, 4)
	require.Equal(t, "answered", refreshed.Entries[2].State)
	require.Equal(t, "pending", refreshed.Entries[3].State)
	answer = any("fourth accepted")
	require.NoError(t, runtime.SubmitResponse(ctx, "history", h.JobKey.JobId, FormResponse{Response: &answer}))
	run()
	latest, err := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "history", JobID: h.JobKey.JobId})
	require.NoError(t, err)
	require.Len(t, latest.Entries, 4)
	for i := range p.Entries {
		require.Equal(t, p.Entries[i].EntryID, latest.Entries[i].EntryID)
		require.Equal(t, "answered", latest.Entries[i].State)
	}
	// Cursor rejection uses the same real retained backend; no invalid token
	// may silently capture a new prefix or access another tenant/job.
	for _, req := range []ListJobInputStoryRequest{
		{TenantID: "foreign", JobID: h.JobKey.JobId, PageToken: page.NextPageToken},
		{TenantID: "history", JobID: "other", PageToken: page.NextPageToken},
	} {
		_, err = s.ListJobInputStory(ctx, req)
		require.ErrorIs(t, err, ErrHistoryInvalidCursor)
	}
	restarted := historyNew(t, ctl)
	_, err = restarted.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "history", JobID: h.JobKey.JobId, PageToken: page.NextPageToken})
	require.ErrorIs(t, err, ErrHistorySnapshotExpired)
	impl := s.(*historyService)
	impl.mu.Lock()
	impl.snapshots = map[string]*historySnapshot{}
	impl.mu.Unlock()
	_, err = s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "history", JobID: h.JobKey.JobId, PageToken: page.NextPageToken})
	require.ErrorIs(t, err, ErrHistorySnapshotExpired)

}

// These are the actual backend mutation/worker entry points. Reads have a
// functioning durable backend but no path to a worker, lease, write or pickup.
type historyReadRuntime struct{ jobdb.WorkflowRuntime }

func (historyReadRuntime) PutChapter(context.Context, jobdb.PutChapterRequest) error {
	panic("history wrote a chapter/artifact")
}
func (historyReadRuntime) SubmitJob(context.Context, jobdb.SubmitJobRequest) (jobdb.JobHandle, error) {
	panic("history submitted child")
}
func (historyReadRuntime) SubmitRestartJob(context.Context, jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	panic("history submitted restart")
}
func (historyReadRuntime) CompleteTaskIfWaiting(context.Context, jobdb.CompleteTaskIfWaitingRequest) error {
	panic("history completed task")
}
func (historyReadRuntime) PollWork(context.Context, jobdb.PollWorkRequest) ([]jobdb.ExecutionLease, error) {
	panic("history started worker/fallback pickup")
}
func (historyReadRuntime) GetJobLease(context.Context, jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	panic("history acquired execution lease")
}

func TestHistorySQLiteUnavailableMembersAndNonmembers(t *testing.T) {
	ctx := celltest.Context(context.Background())
	rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "missing.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rt.Close(ctx)) })
	celltest.Create(t, rt, "tenant", "test", "example.test/test")
	// This producer exists only in the other tenant. Its key must not cross it.
	celltest.Create(t, rt, "foreign", "test", "example.test/test")
	secret := jobdb.NewArtifactFromBytes("secret.txt", []byte("foreign secret"))
	_, err = rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "foreign", JobID: "producer", JobType: "source", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil, secret)}})
	require.NoError(t, err)
	key, err := secret.ArtifactKey()
	require.NoError(t, err)
	form := map[string]any{"question": "missing files", "documents": map[string]any{"foreign-key": artifacts.NewStoredRef(key), "missing-map": artifacts.Ref{Kind: artifacts.RefKindAttached, Name: "lost.txt"}}}
	data, err := workerops.NewInvocationTaskData(workerops.ActivityInvocationRequest{Input: form})
	require.NoError(t, err)
	worker := workflow.ExecutionWorker{JobType: "recipe", Execute: func(c workflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		return c.DoTask(workflow.TaskCall{Site: "collect", TaskType: "input:collect_user_input", ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return data, nil })
	}}
	h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "job", JobType: "recipe", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	runnable, err := workflow.GetJobForRun(ctx, rt, workflow.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: worker, WorkerID: "missing", LeaseDuration: time.Minute})
	require.NoError(t, err)
	_, err = runnable.Run(nil)
	require.NoError(t, err)
	engine, err := workflow.NewEngineBuilder().WithRuntime(historyReadRuntime{WorkflowRuntime: rt}).BuildEngine()
	require.NoError(t, err)
	reader := &historyCountReader{HistoryReader: &c2workflow.SWFWorkflowControl{Engine: engine}}
	s := historyNew(t, reader)
	p, err := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "tenant", JobID: "job"})
	require.NoError(t, err)
	require.Len(t, p.Entries, 1)
	d, err := s.GetJobInputStoryEntry(ctx, GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: p.Entries[0].EntryID, SnapshotToken: p.SnapshotToken})
	require.NoError(t, err)
	require.Len(t, d.RequestManifest, 2)
	require.Zero(t, reader.opens)
	for _, m := range d.RequestManifest {
		_, err = s.OpenJobInputStoryArtifact(ctx, OpenJobInputStoryArtifactRequest{TenantID: "tenant", JobID: "job", EntryID: d.EntryID, ArtifactID: m.ArtifactID, SnapshotToken: p.SnapshotToken})
		require.ErrorIs(t, err, ErrArtifactUnavailable)
	}
	before := reader.opens
	_, err = s.OpenJobInputStoryArtifact(ctx, OpenJobInputStoryArtifactRequest{TenantID: "tenant", JobID: "job", EntryID: d.EntryID, ArtifactID: artifacts.NewStoredRef(key).Identity(), SnapshotToken: p.SnapshotToken})
	require.ErrorIs(t, err, ErrHistoryArtifactNotFound)
	require.Equal(t, before, reader.opens)
	again, err := s.GetJobInputStoryEntry(ctx, GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: d.EntryID, SnapshotToken: p.SnapshotToken})
	require.NoError(t, err)
	require.Equal(t, d.RequestManifest, again.RequestManifest)
}

type historyCountReader struct {
	HistoryReader
	opens int
}

func (r *historyCountReader) GetArtifactLazy(ctx context.Context, tenant string, key jobdb.ArtifactKey) jobdb.Artifact {
	r.opens++
	return r.HistoryReader.GetArtifactLazy(ctx, tenant, key)
}

// Same producer/name at different durable ordinals must never resolve through
// a present-day filename lookup.
func TestHistorySQLiteSameProducerDifferentOrdinals(t *testing.T) {
	ctx := celltest.Context(context.Background())
	rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "ordinals.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rt.Close(ctx)) })
	celltest.Create(t, rt, "tenant", "test", "example.test/test")
	worker := workflow.ExecutionWorker{JobType: "recipe", Execute: func(c workflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		documents := map[string]artifacts.Ref{}
		for _, name := range []string{"first", "second"} {
			result, e := c.DoTask(workflow.TaskCall{Site: name, TaskType: "history:emit", ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return jobdb.NewTaskData(name) })
			if e != nil {
				return nil, e
			}
			attached, e := result.GetArtifacts()
			if e != nil {
				return nil, e
			}
			k, e := attached[0].ArtifactKey()
			if e != nil {
				return nil, e
			}
			documents[name] = artifacts.NewStoredRef(k)
		}
		return c.DoTask(workflow.TaskCall{Site: "collect", TaskType: "input:collect_user_input", ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) {
			return workerops.NewInvocationTaskData(workerops.ActivityInvocationRequest{Input: map[string]any{"question": "files", "documents": documents}})
		})
	}}
	h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "job", JobType: "recipe", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	r, err := workflow.GetJobForRun(ctx, rt, workflow.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: worker, TaskWorkers: []workflow.TaskWorker{historyEmitTask{}}, WorkerID: "ordinal", LeaseDuration: time.Minute})
	require.NoError(t, err)
	_, err = r.Run(nil)
	require.NoError(t, err)
	engine, err := workflow.NewEngineBuilder().WithRuntime(historyReadRuntime{WorkflowRuntime: rt}).BuildEngine()
	require.NoError(t, err)
	s := historyNew(t, &c2workflow.SWFWorkflowControl{Engine: engine})
	p, err := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "tenant", JobID: "job"})
	require.NoError(t, err)
	require.Len(t, p.Entries, 1)
	d, err := s.GetJobInputStoryEntry(ctx, GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: p.Entries[0].EntryID, SnapshotToken: p.SnapshotToken})
	require.NoError(t, err)
	require.Len(t, d.RequestManifest, 2)
	ordinals := map[int64]bool{}
	contents := map[string]bool{}
	for _, m := range d.RequestManifest {
		key, ok := m.Ref.StoredKey()
		require.True(t, ok)
		require.Equal(t, "job", key.JobId)
		require.Equal(t, "same.md", key.Name)
		ordinals[key.TaskOrdinal] = true
		a, e := s.OpenJobInputStoryArtifact(ctx, OpenJobInputStoryArtifactRequest{TenantID: "tenant", JobID: "job", EntryID: d.EntryID, ArtifactID: m.ArtifactID, SnapshotToken: p.SnapshotToken})
		require.NoError(t, e)
		b, e := io.ReadAll(a)
		require.NoError(t, e)
		require.NoError(t, a.Close())
		contents[string(b)] = true
	}
	require.Len(t, ordinals, 2)
	require.Equal(t, map[string]bool{"first": true, "second": true}, contents)
}

type historyEmitTask struct{}

func (historyEmitTask) Name() string { return "history:emit" }
func (historyEmitTask) Run(_ workflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	raw, err := data.GetData()
	if err != nil {
		return nil, err
	}
	var content string
	if err = json.Unmarshal(raw, &content); err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(nil, jobdb.NewArtifactFromBytes("same.md", []byte(content)))
}

// Producer outputs are made by the actual ordinary/guarded/fallback functions,
// then committed through a real execution task. This proves receipt provenance
// and bytes survive durable history, separately from the reader-double tests.
func TestHistorySQLiteReceiptProducerProvenance(t *testing.T) {
	for _, fixture := range historyReceiptFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "receipts.db")})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, rt.Close(ctx)) })
			celltest.Create(t, rt, "tenant", "test", "example.test/test")
			worker := workflow.ExecutionWorker{JobType: "recipe", Execute: func(c workflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
				return c.DoTask(workflow.TaskCall{Site: "receipt", TaskType: fixture.taskType, ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) {
					return &jobdb.SimpleTaskData{Data: historySnap(fixture.form).Data}, nil
				})
			}}
			h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "job", JobType: "recipe", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
			require.NoError(t, err)
			runnable, err := workflow.GetJobForRun(ctx, rt, workflow.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: worker, TaskWorkers: []workflow.TaskWorker{historyReceiptTask{fixture}}, WorkerID: "receipt", LeaseDuration: time.Minute})
			require.NoError(t, err)
			_, err = runnable.Run(nil)
			require.NoError(t, err)
			engine, err := workflow.NewEngineBuilder().WithRuntime(historyReadRuntime{WorkflowRuntime: rt}).BuildEngine()
			require.NoError(t, err)
			s := historyNew(t, &c2workflow.SWFWorkflowControl{Engine: engine})
			assertHistoryReceipt(t, s, ctx, "tenant", "job", fixture)
		})
	}
}

type historyReceiptTask struct{ fixture historyReceiptFixture }

func (t historyReceiptTask) Name() string { return t.fixture.taskType }
func (t historyReceiptTask) Run(workflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error) {
	return &jobdb.SimpleTaskData{Data: historyOut(t.fixture.output).Data}, nil
}

// Publication output here is a retained fixture, not a claim this test runs Git.
// The Git transport suite establishes the writer behavior independently.
type historyMergeTask struct{}

func (historyMergeTask) Name() string { return "merge:merge" }
func (historyMergeTask) Run(_ workflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	return &jobdb.SimpleTaskData{Data: historyOut(strings.Replace(mergeEvidence, `"candidate_subject":"actual",`, "", 1)).Data}, nil
}

func TestHistorySQLiteSharedMergeSnapshotDuringPagination(t *testing.T) {
	ctx := celltest.Context(context.Background())
	rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "shared.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rt.Close(ctx)) })
	celltest.Create(t, rt, "tenant", "test", "example.test/test")
	engine, err := workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	ctl := &c2workflow.SWFWorkflowControl{Engine: engine}
	base := structuredWorker{forms: []InputForm{{Question: "first", Type: FieldTypeBoolean}, {Question: "second", Type: FieldTypeBoolean}}}.executionWorker()
	worker := base
	worker.Execute = func(c workflow.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
		emit := func(site string) error {
			_, e := c.DoTask(workflow.TaskCall{Site: site, TaskType: "merge:merge", ContractVersion: 1, Command: []byte(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return jobdb.NewTaskData(nil) })
			return e
		}
		if e := emit("before"); e != nil {
			return nil, e
		}
		result, e := base.Execute(c, in)
		if e != nil {
			return nil, e
		}
		if e := emit("after"); e != nil {
			return nil, e
		}
		return result, nil
	}
	h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "job", JobType: "recipe", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	run := func() {
		r, e := workflow.GetJobForRun(ctx, rt, workflow.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: worker, TaskWorkers: []workflow.TaskWorker{historyMergeTask{}}, WorkerID: "shared", LeaseDuration: time.Minute})
		require.NoError(t, e)
		_, e = r.Run(nil)
		require.NoError(t, e)
	}
	finish := func() {
		w, e := ctl.GetWaitingTask(ctx, h.JobKey)
		require.NoError(t, e)
		require.NoError(t, w.Finish(ctx, &jobdb.SimpleTaskData{Data: historyOut(`{"response":true}`).Data}))
	}
	run()
	finish()
	run()
	s := historyNew(t, ctl)
	page, err := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "tenant", JobID: "job", PageSize: 1})
	require.NoError(t, err)
	require.NotEmpty(t, page.NextPageToken)
	frozen, err := GetJobHistorySnapshot(ctx, s, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job", SnapshotToken: page.SnapshotToken})
	require.NoError(t, err)
	require.Len(t, frozen.Merges, 1)
	require.NotNil(t, frozen.Merges[0].CompletedAt)
	require.Equal(t, "recorded", frozen.Merges[0].EvidenceAvailability)
	require.Nil(t, frozen.Merges[0].CandidateSubject)
	finish()
	run()
	next, err := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "tenant", JobID: "job", PageSize: 1, PageToken: page.NextPageToken})
	require.NoError(t, err)
	require.Equal(t, "pending", next.Entries[0].State)
	old, err := GetJobHistorySnapshot(ctx, s, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job", SnapshotToken: next.SnapshotToken})
	require.NoError(t, err)
	require.Equal(t, frozen, old)
	current, err := GetJobHistorySnapshot(ctx, s, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job"})
	require.NoError(t, err)
	require.Len(t, current.Merges, 2)
}
