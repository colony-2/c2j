package input

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type historyFixtureReader struct {
	mu       sync.Mutex
	run      jobdb.GetJobRunResponse
	err      error
	wait     workflowctl.TaskHandle
	waitErr  error
	artifact func(string, jobdb.ArtifactKey) jobdb.Artifact
}

func (r *historyFixtureReader) GetJobRun(_ context.Context, req jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return jobdb.GetJobRunResponse{}, r.err
	}
	if req.JobKey != r.run.Job.JobKey {
		return jobdb.GetJobRunResponse{}, jobdb.ErrJobNotFound
	}
	return historyCopy(r.run), nil
}
func (r *historyFixtureReader) GetWaitingTask(context.Context, jobdb.JobKey) (workflowctl.TaskHandle, error) {
	return r.wait, r.waitErr
}
func (r *historyFixtureReader) GetArtifactLazy(_ context.Context, tenant string, key jobdb.ArtifactKey) jobdb.Artifact {
	if r.artifact == nil {
		panic("unexpected artifact byte read")
	}
	return r.artifact(tenant, key)
}
func historyRaw(s string) json.RawMessage { return json.RawMessage(s) }
func historyRecord(n int64, kind, id string) jobdb.ExecutionHistoryEntry {
	return jobdb.ExecutionHistoryEntry{Ordinal: n, Record: jobdb.ExecutionRecord{Version: jobdb.ExecutionPatternVersion, Kind: kind, ID: id}}
}
func historyTask(n int64, id, typ string) jobdb.ExecutionHistoryEntry {
	e := historyRecord(n, "task", id)
	e.Record.Descriptor = historyRaw(fmt.Sprintf(`{"call":{"task_type":%q}}`, typ))
	return e
}
func historySnap(form string) *jobdb.ExecutionSnapshot {
	return &jobdb.ExecutionSnapshot{Data: historyRaw(`{"input":` + form + `}`)}
}
func historyOut(out string) *jobdb.ExecutionSnapshot {
	return &jobdb.ExecutionSnapshot{Data: historyRaw(`{"v":1,"kind":"activity_output","payload":{"output":` + out + `}}`)}
}
func historyWait(n int64, id, form string) jobdb.ExecutionHistoryEntry {
	e := historyRecord(n, "wait", id)
	e.Record.Input = historySnap(form)
	return e
}
func historyResult(n int64, id, form, out string) jobdb.ExecutionHistoryEntry {
	e := historyRecord(n, "result", id)
	e.Record.Input = historySnap(form)
	e.Record.Output = historyOut(out)
	return e
}
func historyFixture(records ...jobdb.ExecutionHistoryEntry) *historyFixtureReader {
	return &historyFixtureReader{run: jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}}, Execution: records}}
}
func historyNew(t *testing.T, r HistoryReader) HistoryService {
	t.Helper()
	s, err := NewHistoryService(HistoryConfig{Reader: r})
	require.NoError(t, err)
	return s
}
func historyList(t *testing.T, s HistoryService) *JobInputStoryPage {
	t.Helper()
	p, err := s.ListJobInputStory(context.Background(), ListJobInputStoryRequest{TenantID: "tenant", JobID: "job"})
	require.NoError(t, err)
	return p
}

func TestHistoryLineageAndLosslessValues(t *testing.T) {
	// RI-014, RI-032, RI-041–RI-046; C2J-001, C2J-003–004 (service units).
	scope := `/scope:"op"#1/attempt:2`
	gen := scope + `/task:"input:generate_form"#1`
	collect := scope + `/task:"input:collect_user_input"#1`
	form := `{"question":"values","default":9007199254740993,"response_schema":null,"context":{}}`
	r := historyFixture(historyTask(1, gen, "input:generate_form"), historyResult(2, gen+"/attempt:1", `{}`, form), historyTask(3, collect, "input:collect_user_input"), historyWait(4, collect+"/attempt:1", form))
	s := historyNew(t, r)
	p := historyList(t, s)
	require.Len(t, p.Entries, 1)
	id := p.Entries[0].EntryID
	require.Equal(t, "pending", p.Entries[0].State)
	require.Empty(t, p.Entries[0].RequestID)
	require.False(t, p.Entries[0].Summary.Present)
	r.mu.Lock()
	failed := historyResult(5, collect+"/attempt:1", form, `{"response":"failed"}`)
	failed.Record.ErrorKind = "SystemError"
	r.run.Execution = append(r.run.Execution, failed, historyWait(6, collect+"/attempt:2", form), historyResult(7, collect+"/attempt:2", form, `{"response":null,"fields":{"f":false,"z":0,"s":"","a":[],"o":{},"n":9007199254740993},"user_id":"reported"}`))
	r.run.Job.Status = jobdb.JobStatusCompleted
	r.mu.Unlock()
	fresh := historyList(t, s)
	require.Len(t, fresh.Entries, 1)
	require.Equal(t, id, fresh.Entries[0].EntryID)
	require.Equal(t, "answered", fresh.Entries[0].State)
	require.Equal(t, "reported_legacy", fresh.Entries[0].Actor.Provenance)
	require.Contains(t, string(fresh.Entries[0].AcceptedResponse.Raw), "9007199254740993")
	d, err := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: id})
	require.NoError(t, err)
	require.Contains(t, string(d.RawForm), "9007199254740993")
	require.Equal(t, "read_only", d.Actionability.Observation)
	require.Empty(t, d.Locator.StoryNodeID)
	old, err := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: id, SnapshotToken: p.SnapshotToken})
	require.NoError(t, err)
	require.Equal(t, "pending", old.State)
	require.False(t, old.AcceptedResponse.Present)
	// A separately invoked collection reusing the frozen generation is a new turn.
	r.mu.Lock()
	second := scope + `/task:"input:collect_user_input"#2`
	r.run.Execution = append(r.run.Execution, historyTask(8, second, "input:collect_user_input"), historyWait(9, second+"/attempt:1", form))
	r.mu.Unlock()
	more := historyList(t, s)
	require.Len(t, more.Entries, 2)
	require.NotEqual(t, id, more.Entries[1].EntryID)
}
func TestHistoryGenerationRetriesAndAmbiguity(t *testing.T) {
	scope := `/scope:"repeat"#2/attempt:1`
	gen := scope + `/task:"input:generate_form"#1`
	c := scope + `/task:"input:collect_user_input"#1`
	form := `{"title":"identical","fields":[]}`
	r := historyFixture(historyTask(1, gen, "input:generate_form"), historyResult(2, gen+"/attempt:1", `{}`, form), historyResult(3, gen+"/attempt:2", `{}`, form), historyTask(4, c, "input:collect_user_input"), historyWait(5, c+"/attempt:1", form))
	p := historyList(t, historyNew(t, r))
	require.Len(t, p.Entries, 3)
	require.Equal(t, "partial", p.Completeness)
	require.Equal(t, "ambiguous_lineage", p.Diagnostics[0].Code)
}
func TestHistoryAcceptedSourcesAndDiagnostics(t *testing.T) {
	form := `{"question":"choice","output":{"response":"not accepted"},"fallback_at":"2000-01-01T00:00:00Z"}`
	for _, typ := range []string{"auto-fill-input:echo", "input-alternate:complete", "input:collect_user_input"} {
		t.Run(typ, func(t *testing.T) {
			id := `/task:"answer"#1`
			r := historyFixture(historyTask(1, id, typ), historyWait(2, id+"/attempt:1", form))
			s := historyNew(t, r)
			p := historyList(t, s)
			require.False(t, p.Entries[0].AcceptedResponse.Present)
			require.Equal(t, "pending", p.Entries[0].State)
			r.run.Execution = append(r.run.Execution, historyResult(3, id+"/attempt:1", form, `{"response":false}`))
			p = historyList(t, s)
			require.True(t, p.Entries[0].AcceptedResponse.Present)
			require.Equal(t, "unavailable", p.Entries[0].Actor.Provenance)
			switch typ {
			case "input-alternate:complete":
				require.Equal(t, "defaulted", p.Entries[0].State)
				require.Equal(t, "unanswered_fallback", p.Entries[0].ResponseSource)
			case "auto-fill-input:echo":
				require.Equal(t, "autofill", p.Entries[0].ResponseSource)
			default:
				require.Equal(t, "external_submission", p.Entries[0].ResponseSource)
			}
		})
	}
	r := historyFixture(historyTask(1, "generator", "input:generate_form"), historyRecord(2, "result", "generator/attempt:1"))
	p := historyList(t, historyNew(t, r))
	require.Empty(t, p.Entries)
	require.Equal(t, "partial", p.Completeness)
	require.NotEmpty(t, p.Diagnostics)
	r.run.Execution[0].Record.Version = 99
	_, err := historyNew(t, r).ListJobInputStory(context.Background(), ListJobInputStoryRequest{TenantID: "tenant", JobID: "job"})
	require.ErrorIs(t, err, ErrHistoryUnsupported)
	storage := errors.New("storage denied")
	r.err = storage
	_, err = historyNew(t, r).ListJobInputStory(context.Background(), ListJobInputStoryRequest{TenantID: "tenant", JobID: "job"})
	require.ErrorIs(t, err, storage)
}
func TestHistoryCursorIsolationAndConcurrentRefresh(t *testing.T) {
	r := historyFixture()
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("/task:%d", i)
		r.run.Execution = append(r.run.Execution, historyTask(int64(2*i-1), id, "input:collect_user_input"), historyWait(int64(2*i), id+"/attempt:1", `{"question":"q"}`))
	}
	s := historyNew(t, r)
	p, err := s.ListJobInputStory(context.Background(), ListJobInputStoryRequest{TenantID: "tenant", JobID: "job", PageSize: 1})
	require.NoError(t, err)
	// Barriers establish that mutation commits between pages; no timing sleeps.
	ready, done := make(chan struct{}), make(chan struct{})
	go func() {
		<-ready
		r.mu.Lock()
		r.run.Execution = append(r.run.Execution, historyResult(7, "/task:3/attempt:1", `{"question":"q"}`, `{"response":0}`), historyTask(8, "/task:4", "input:collect_user_input"), historyWait(9, "/task:4/attempt:1", `{"question":"new"}`))
		r.mu.Unlock()
		close(done)
	}()
	close(ready)
	<-done
	ids := []string{p.Entries[0].EntryID}
	token := p.NextPageToken
	for token != "" {
		page, e := s.ListJobInputStory(context.Background(), ListJobInputStoryRequest{TenantID: "tenant", JobID: "job", PageSize: 1, PageToken: token})
		require.NoError(t, e)
		require.Equal(t, p.SnapshotToken, page.SnapshotToken)
		require.Equal(t, "pending", page.Entries[0].State)
		ids = append(ids, page.Entries[0].EntryID)
		token = page.NextPageToken
	}
	require.Len(t, ids, 3)
	require.NotEqual(t, ids[0], ids[1])
	fresh := historyList(t, s)
	require.Len(t, fresh.Entries, 4)
	require.Equal(t, "answered", fresh.Entries[2].State)
	for _, req := range []ListJobInputStoryRequest{{TenantID: "other", JobID: "job", PageToken: p.NextPageToken}, {TenantID: "tenant", JobID: "other", PageToken: p.NextPageToken}, {TenantID: "tenant", JobID: "job", PageToken: p.NextPageToken + "tamper"}} {
		_, err := s.ListJobInputStory(context.Background(), req)
		require.ErrorIs(t, err, ErrHistoryInvalidCursor)
	}
	impl := s.(*historyService)
	impl.mu.Lock()
	impl.snapshots = map[string]*historySnapshot{}
	impl.mu.Unlock()
	_, err = s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: ids[0], SnapshotToken: p.SnapshotToken})
	require.ErrorIs(t, err, ErrHistorySnapshotExpired)
}
func TestHistoryManifestMembershipAndReadSafety(t *testing.T) {
	key := jobdb.ArtifactKey{JobId: "child", TaskOrdinal: 7, Name: "same.md", SizeBytes: 5}
	ref, _ := json.Marshal(artifacts.NewStoredRef(key))
	form := `{"kind":"review","request_id":"r","title":"saved","documents":{"first":` + string(ref) + `,"alias":` + string(ref) + `,"external":{"kind":"external","name":"web","external":{"url":"https://forbidden.invalid/schema"}},"attached":{"kind":"attached","name":"missing.md"}},"context":{"artifacts_glob":[{"pattern":"**/*"}]}}`
	r := historyFixture(historyTask(1, "/task:collect", "input:collect_user_input"), historyWait(2, "/task:collect/attempt:1", form))
	opens := 0
	r.artifact = func(tenant string, k jobdb.ArtifactKey) jobdb.Artifact {
		require.Equal(t, "tenant", tenant)
		require.Equal(t, key, k)
		opens++
		return jobdb.NewArtifactFromBytes(k.Name, []byte("child"))
	}
	s := historyNew(t, r)
	p := historyList(t, s)
	require.Zero(t, opens)
	d, err := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: p.Entries[0].EntryID})
	require.NoError(t, err)
	require.False(t, d.Actionability.Actionable)
	require.Zero(t, opens)
	require.Len(t, d.RequestManifest, 4)
	var member HistoryArtifactMember
	for _, m := range d.RequestManifest {
		if m.Ref.Identity() == artifacts.NewStoredRef(key).Identity() {
			member = m
		}
	}
	require.Len(t, member.Associations, 2)
	open := OpenJobInputStoryArtifactRequest{TenantID: "tenant", JobID: "job", EntryID: d.EntryID, ArtifactID: member.ArtifactID, SnapshotToken: d.SnapshotToken}
	a, err := s.OpenJobInputStoryArtifact(context.Background(), open)
	require.NoError(t, err)
	b, err := io.ReadAll(a)
	require.NoError(t, err)
	require.NoError(t, a.Close())
	require.Equal(t, "child", string(b))
	require.Equal(t, 1, opens)
	open.ArtifactID = "arbitrary-stored-key"
	_, err = s.OpenJobInputStoryArtifact(context.Background(), open)
	require.ErrorIs(t, err, ErrHistoryArtifactNotFound)
	require.Equal(t, 1, opens)
	open.ArtifactID = member.ArtifactID
	open.TenantID = "foreign"
	_, err = s.OpenJobInputStoryArtifact(context.Background(), open)
	require.ErrorIs(t, err, ErrHistoryInvalidCursor)
	require.Equal(t, 1, opens)
	// Mutating the returned page/detail cannot change immutable cached values.
	d.FormFields["title"] = historyRaw(`"mutated"`)
	d.RequestManifest[0].Ref.Name = "mutated"
	again, err := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: d.EntryID, SnapshotToken: d.SnapshotToken})
	require.NoError(t, err)
	require.Equal(t, `"saved"`, string(again.FormFields["title"]))
}
func TestHistoryPrivateRawRestoration(t *testing.T) {
	form := `{"question":"` + strings.Repeat("x", 140000) + `","default":9007199254740993,"unknown":null}`
	e := historyWait(2, "/task:collect/attempt:1", `{}`)
	key := jobdb.ArtifactKey{JobId: "job", TaskOrdinal: 2, Name: "c2j-invocation-input.json", SizeBytes: int64(len(form) + 10)}
	e.Record.Input = &jobdb.ExecutionSnapshot{Data: historyRaw(`{"input":null,"input_artifact":"c2j-invocation-input.json"}`), Artifacts: []jobdb.ExecutionArtifact{{Name: key.Name, Key: &key}}}
	r := historyFixture(historyTask(1, "/task:collect", "input:collect_user_input"), e)
	r.artifact = func(string, jobdb.ArtifactKey) jobdb.Artifact {
		return jobdb.NewArtifactFromBytes(key.Name, []byte(`{"input":`+form+`}`))
	}
	s := historyNew(t, r)
	p := historyList(t, s)
	require.Len(t, p.Entries, 1)
	require.Equal(t, "9007199254740993", string(p.Entries[0].FormFields["default"]))
	require.Equal(t, "null", string(p.Entries[0].FormFields["unknown"]))
	d, err := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: p.Entries[0].EntryID, SnapshotToken: p.SnapshotToken})
	require.NoError(t, err)
	require.Empty(t, d.RequestManifest)
}

// Keep timeout configuration tested without wall-clock sleeps.
func TestHistoryExpirationAndErrors(t *testing.T) {
	r := historyFixture(historyTask(1, "/task:c", "input:collect_user_input"), historyWait(2, "/task:c/attempt:1", `{"question":"q"}`))
	s := historyNew(t, r)
	p := historyList(t, s)
	impl := s.(*historyService)
	impl.mu.Lock()
	for _, snap := range impl.snapshots {
		snap.created = time.Now().Add(-time.Hour)
	}
	impl.mu.Unlock()
	_, err := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: p.Entries[0].EntryID, SnapshotToken: p.SnapshotToken})
	require.ErrorIs(t, err, ErrHistorySnapshotExpired)
	_, err = s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: "unknown"})
	require.ErrorIs(t, err, ErrHistoryEntryNotFound)
}

// A reader may implement extra capabilities, but history must never invoke
// them. The task's mutator is independently poisoned as well.
type historyGuardReader struct{ HistoryReader }

func (historyGuardReader) Execute()        { panic("history executed an operation") }
func (historyGuardReader) WriteArtifact()  { panic("history wrote an artifact") }
func (historyGuardReader) SubmitChild()    { panic("history submitted a child") }
func (historyGuardReader) PickupFallback() { panic("history picked up fallback") }
func (historyGuardReader) FetchSchema()    { panic("history fetched a remote schema") }
func (historyGuardReader) Replay()         { panic("history replayed the recipe") }
func (historyGuardReader) CompleteTask(context.Context, jobdb.JobKey, int64, string, any) error {
	panic("history completed a task")
}
func (r historyGuardReader) GetWaitingTask(ctx context.Context, key jobdb.JobKey) (workflowctl.TaskHandle, error) {
	task, err := r.HistoryReader.GetWaitingTask(ctx, key)
	if task == nil {
		return nil, err
	}
	return historyGuardTask{TaskHandle: task}, err
}

type historyGuardTask struct{ workflowctl.TaskHandle }

func (historyGuardTask) Finish(context.Context, jobdb.TaskData) error {
	panic("history finished a task")
}

func TestHistoryLiveWaitAndMapping(t *testing.T) {
	form := `{"kind":"review","request_id":"r","title":"guarded","fields":[]}`
	r := historyFixture(historyTask(1, "/task:c", "input:collect_user_input"), historyWait(2, "/task:c/attempt:1", form))
	task := &clientTask{key: r.run.Job.JobKey, ordinal: 3, data: &jobdb.SimpleTaskData{Data: historySnap(form).Data}, finish: func(jobdb.TaskData) error { panic("history mutation") }}
	r.wait = task
	s, err := NewHistoryService(HistoryConfig{Reader: historyGuardReader{r}, MapNode: func(_ context.Context, key jobdb.JobKey, loc HistoryLocator) (string, error) {
		require.Equal(t, r.run.Job.JobKey, key)
		require.Equal(t, []string{"/task:c"}, loc.CollectionInvocationIDs)
		return "correct-node-attempt", nil
	}})
	require.NoError(t, err)
	p := historyList(t, s)
	require.Equal(t, "correct-node-attempt", p.Entries[0].Locator.StoryNodeID)
	get := func() *JobInputStoryEntryDetail {
		d, e := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: p.Entries[0].EntryID, SnapshotToken: p.SnapshotToken})
		require.NoError(t, e)
		return d
	}
	require.True(t, get().Actionability.Actionable)
	task.ordinal = 4
	require.False(t, get().Actionability.Actionable)
	task.ordinal = 3
	task.data = &jobdb.SimpleTaskData{Data: historySnap(`{"kind":"review","request_id":"replacement"}`).Data}
	require.False(t, get().Actionability.Actionable)
	task.data = &jobdb.SimpleTaskData{Data: historySnap(form).Data}
	r.waitErr = errors.New("wait unavailable")
	require.False(t, get().Actionability.Actionable)
	r.waitErr = nil
	r.wait = nil
	require.False(t, get().Actionability.Actionable)
	s, err = NewHistoryService(HistoryConfig{Reader: r, MapNode: func(context.Context, jobdb.JobKey, HistoryLocator) (string, error) {
		return "", errors.New("replay unavailable")
	}})
	require.NoError(t, err)
	p = historyList(t, s)
	require.Len(t, p.Entries, 1)
	require.Empty(t, p.Entries[0].Locator.StoryNodeID)
}
func TestHistoryMissingBytesAndPrivateMembership(t *testing.T) {
	key := jobdb.ArtifactKey{JobId: "lost", TaskOrdinal: 2, Name: "lost.md", SizeBytes: 10}
	ref, _ := json.Marshal(artifacts.NewStoredRef(key))
	form := `{"question":"files","documents":{"lost":` + string(ref) + `}}`
	r := historyFixture(historyTask(1, "/task:c", "input:collect_user_input"), historyWait(2, "/task:c/attempt:1", form))
	r.artifact = func(string, jobdb.ArtifactKey) jobdb.Artifact { return nil }
	s := historyNew(t, r)
	p := historyList(t, s)
	d, e := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: p.Entries[0].EntryID})
	require.NoError(t, e)
	require.Len(t, d.RequestManifest, 1)
	_, e = s.OpenJobInputStoryArtifact(context.Background(), OpenJobInputStoryArtifactRequest{TenantID: "tenant", JobID: "job", EntryID: d.EntryID, ArtifactID: d.RequestManifest[0].ArtifactID, SnapshotToken: d.SnapshotToken})
	require.ErrorIs(t, e, ErrArtifactUnavailable)
	again, e := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: d.EntryID, SnapshotToken: d.SnapshotToken})
	require.NoError(t, e)
	require.Equal(t, d.RequestManifest, again.RequestManifest)
}

func TestHistoryFrozenGenerationManifestAndChildMap(t *testing.T) {
	scope := `/scope:"op"#1/attempt:1`
	gen := scope + `/task:"input:generate_form"#1`
	c := scope + `/task:"input:collect_user_input"#1`
	child := artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "child", TaskOrdinal: 7, Name: "file.md", SizeBytes: 3})
	ref, _ := json.Marshal(child)
	form := `{"request":{"child":{"Outputs":{"artifact_refs":{"file.md":{"kind":"attached","name":"file.md"}}},"Artifacts":{"file.md":` + string(ref) + `}}},"response_schema":{}}`
	r := historyFixture(historyTask(1, gen, "input:generate_form"), historyResult(2, gen+"/attempt:1", `{}`, form))
	s := historyNew(t, r)
	p := historyList(t, s)
	require.Len(t, p.Entries, 1)
	id := p.Entries[0].EntryID
	before, e := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: id, SnapshotToken: p.SnapshotToken})
	require.NoError(t, e)
	require.Len(t, before.RequestManifest, 1)
	require.Equal(t, child, before.RequestManifest[0].Ref)
	require.NotNil(t, before.RequestManifest[0].OriginalRef)
	wait := historyWait(4, c+"/attempt:1", form)
	key := jobdb.ArtifactKey{JobId: "job", TaskOrdinal: 4, Name: "later.txt", SizeBytes: 4}
	keys, _ := json.Marshal([]jobdb.ArtifactKey{key})
	wait.Record.Input.Data = historyRaw(`{"input":` + form + `,"artifact_keys":` + string(keys) + `}`)
	r.run.Execution = append(r.run.Execution, historyTask(3, c, "input:collect_user_input"), wait)
	after := historyList(t, s)
	require.Len(t, after.Entries, 1)
	require.Equal(t, id, after.Entries[0].EntryID)
	detail, e := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: id})
	require.NoError(t, e)
	require.Equal(t, before.RequestManifest, detail.RequestManifest)
}

func TestHistoryCursorRestartAndEviction(t *testing.T) {
	r := historyFixture(historyTask(1, "/task:c", "input:collect_user_input"), historyWait(2, "/task:c/attempt:1", `{"question":"q"}`))
	s, err := NewHistoryService(HistoryConfig{Reader: r, MaxSnapshots: 1})
	require.NoError(t, err)
	p := historyList(t, s)
	restarted := historyNew(t, r)
	_, err = restarted.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: p.Entries[0].EntryID, SnapshotToken: p.SnapshotToken})
	require.ErrorIs(t, err, ErrHistorySnapshotExpired)
	historyList(t, s)
	_, err = s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: p.Entries[0].EntryID, SnapshotToken: p.SnapshotToken})
	require.ErrorIs(t, err, ErrHistorySnapshotExpired)
	impl := s.(*historyService)
	for _, c := range []historyCursor{
		{Version: 2, Tenant: "tenant", Job: "job", Snapshot: "x"},
		{Version: 1, Tenant: "tenant", Job: "other", Snapshot: "x"},
		{Version: 1, Tenant: "foreign", Job: "job", Snapshot: "x"},
	} {
		_, err = s.ListJobInputStory(context.Background(), ListJobInputStoryRequest{TenantID: "tenant", JobID: "job", PageToken: impl.historyToken(c)})
		require.ErrorIs(t, err, ErrHistoryInvalidCursor)
	}
}

func TestHistoryGenerationDirectAttachments(t *testing.T) {
	scope := `/scope:"op"#1/attempt:1`
	gen := scope + `/task:"input:generate_form"#1`
	record := historyResult(2, gen+"/attempt:1", `{}`, `{"question":"q"}`)
	for _, key := range []jobdb.ArtifactKey{
		{JobId: "job", TaskOrdinal: 2, Name: "generated.txt", SizeBytes: 3},
		{JobId: "job", TaskOrdinal: 1, Name: "inherited.txt", SizeBytes: 3},
		{JobId: "job", TaskOrdinal: 2, Name: "c2j-invocation-input.json", SizeBytes: 3},
	} {
		k := key
		record.Record.Output.Artifacts = append(record.Record.Output.Artifacts, jobdb.ExecutionArtifact{Name: k.Name, Key: &k})
	}
	s := historyNew(t, historyFixture(historyTask(1, gen, "input:generate_form"), record))
	p := historyList(t, s)
	d, err := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: p.Entries[0].EntryID, SnapshotToken: p.SnapshotToken})
	require.NoError(t, err)
	require.Len(t, d.RequestManifest, 1)
	require.Equal(t, "generated.txt", d.RequestManifest[0].Ref.NameValue())
	require.Equal(t, "step_output", d.RequestManifest[0].Associations[0].Kind)
}

func TestHistoryExplicitBindingAndAttachmentSources(t *testing.T) {
	scope := `/scope:"op"#1/attempt:1`
	gen := scope + `/task:"input:generate_form"#1`
	collect := scope + `/task:"input:collect_user_input"#1`
	key := func(name string, n int64) jobdb.ArtifactKey {
		return jobdb.ArtifactKey{JobId: "job", TaskOrdinal: n, Name: name, SizeBytes: 3}
	}
	ref := func(name string, n int64) string {
		b, _ := json.Marshal(artifacts.NewStoredRef(key(name, n)))
		return string(b)
	}
	generation := historyResult(2, gen+"/attempt:1", `{}`, `{"question":"q"}`)
	keys, _ := json.Marshal([]jobdb.ArtifactKey{key("explicit-key", 1)})
	generation.Record.Input.Data = historyRaw(`{"input":{},"artifacts":{"binding":` + ref("binding", 1) + `},"artifact_keys":` + string(keys) + `}`)
	for _, k := range []jobdb.ArtifactKey{key("generated", 2), key("inherited", 1), key("c2j-invocation-input.json", 2)} {
		k := k
		generation.Record.Output.Artifacts = append(generation.Record.Output.Artifacts, jobdb.ExecutionArtifact{Name: k.Name, Key: &k})
	}
	answer := historyResult(5, collect+"/attempt:1", `{"question":"q"}`, `{"fields":{"file":`+ref("answer-file", 5)+`}}`)
	for _, k := range []jobdb.ArtifactKey{key("direct-output", 5), key("inherited-output", 2), key("__c2j_objects__/private", 5)} {
		k := k
		answer.Record.Output.Artifacts = append(answer.Record.Output.Artifacts, jobdb.ExecutionArtifact{Name: k.Name, Key: &k})
	}
	s := historyNew(t, historyFixture(historyTask(1, gen, "input:generate_form"), generation, historyTask(3, collect, "input:collect_user_input"), historyWait(4, collect+"/attempt:1", `{"question":"q"}`), answer))
	p := historyList(t, s)
	require.Len(t, p.Entries, 1)
	d, err := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: p.Entries[0].EntryID, SnapshotToken: p.SnapshotToken})
	require.NoError(t, err)
	request := map[string]string{}
	response := map[string]string{}
	for _, m := range d.RequestManifest {
		request[m.Ref.NameValue()] = m.Associations[0].Kind
	}
	for _, m := range d.ResponseManifest {
		response[m.Ref.NameValue()] = m.Associations[0].Kind
	}
	require.Equal(t, map[string]string{"binding": "step_input", "explicit-key": "step_input", "generated": "step_output"}, request)
	require.Equal(t, map[string]string{"answer-file": "response_attachment", "direct-output": "step_output"}, response)
}

func TestHistoryMismatchedFrozenFormDiagnostics(t *testing.T) {
	scope := `/scope:"op"#1/attempt:1`
	gen := scope + `/task:"input:generate_form"#1`
	c := scope + `/task:"input:collect_user_input"#1`
	s := historyNew(t, historyFixture(historyTask(1, gen, "input:generate_form"), historyResult(2, gen+"/attempt:1", `{}`, `{"question":"q","default":9007199254740993}`), historyTask(3, c, "input:collect_user_input"), historyWait(4, c+"/attempt:1", `{"question":"q","default":9007199254740992}`)))
	p := historyList(t, s)
	require.Len(t, p.Entries, 2)
	require.Equal(t, "partial", p.Completeness)
	require.Equal(t, "frozen_form_mismatch", p.Diagnostics[0].Code)
	unknown := historyNew(t, historyFixture(historyTask(1, "/task:c", "input:collect_user_input"), historyWait(2, "/task:c/attempt:1", `{"kind":"future"}`)))
	p = historyList(t, unknown)
	require.Equal(t, "unknown", p.Entries[0].Kind)
	require.Equal(t, "unknown_form_kind", p.Diagnostics[0].Code)
}

func TestHistoryMalformedRecordsAndOutputVersion(t *testing.T) {
	malformed := historyTask(1, "/task:broken", "input:collect_user_input")
	malformed.Record.Descriptor = historyRaw(`{"call":false}`)
	s := historyNew(t, historyFixture(malformed))
	p := historyList(t, s)
	require.Equal(t, "partial", p.Completeness)
	require.Equal(t, "malformed_task", p.Diagnostics[0].Code)
	accepted := historyResult(3, "/task:c/attempt:1", `{"question":"q"}`, `{"response":false}`)
	accepted.Record.Output.Data = historyRaw(`{"v":99,"kind":"activity_output","payload":{"output":{"response":false}}}`)
	s = historyNew(t, historyFixture(historyTask(1, "/task:c", "input:collect_user_input"), historyWait(2, "/task:c/attempt:1", `{"question":"q"}`), accepted))
	_, err := s.ListJobInputStory(context.Background(), ListJobInputStoryRequest{TenantID: "tenant", JobID: "job"})
	require.ErrorIs(t, err, ErrHistoryUnsupported)
	var capability *HistoryError
	require.ErrorAs(t, err, &capability)
	require.Equal(t, "activity_output_version", capability.Capability)
}
