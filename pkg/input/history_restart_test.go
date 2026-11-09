package input

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/colony-2/c2j/pkg/artifacts"
	"io"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func historyScopeRecord(n int64, id string) jobdb.ExecutionHistoryEntry {
	e := historyRecord(n, "scope", id)
	e.Record.Descriptor = historyRaw(`{"restart":true}`)
	return e
}

func TestRecordedHistoryRestartIdentities(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	first := `/scope:"$job"#1/attempt:1/scope:"review"#1`
	second := `/scope:"$job"#1/attempt:2/scope:"review"#1`
	id := first + `/attempt:2/task:"generate"#1/attempt:1`
	ordinal := int64(9)
	run := historyFixture(historyScopeRecord(9, first), historyScopeRecord(9, second), historyRecord(9, "result", id), historyRecord(9, "attempt", id)).run
	loc := HistoryLocator{InvocationID: id, GenerationOrdinal: &ordinal, JobAttempt: 1, TaskAttempt: 1}
	a := RecordedHistoryRestart(key, run, loc)
	require.Equal(t, first, a.ScopeID)
	require.Equal(t, HistoryRestartContaining, a.Relationship)
	require.Empty(t, a.UnavailableReason)
	// A scope occurrence itself is an exact boundary, including compact records.
	loc.InvocationID = second
	a = RecordedHistoryRestart(key, run, loc)
	require.Equal(t, second, a.ScopeID)
	require.Equal(t, HistoryRestartExact, a.Relationship)
	loc.InvocationID = id
	run.Execution = run.Execution[1:]
	require.Empty(t, RecordedHistoryRestart(key, run, loc).ScopeID, "never substitute another job attempt")
	run.Execution = append(run.Execution, historyScopeRecord(9, first), historyScopeRecord(9, first))
	require.Contains(t, RecordedHistoryRestart(key, run, loc).UnavailableReason, "ambiguous")
	require.NotEmpty(t, RecordedHistoryRestart(jobdb.JobKey{TenantId: "tenant", JobId: "other"}, run, loc).UnavailableReason)
	loc.GenerationOrdinal = nil
	require.NotEmpty(t, RecordedHistoryRestart(key, run, loc).UnavailableReason)
}

func TestHistoryRestartMappingPreservesReadability(t *testing.T) {
	scope := `/scope:"review"#1`
	task := scope + `/attempt:1/task:"input:collect_user_input"#1`
	r := historyFixture(historyScopeRecord(1, scope), historyScopeRecord(1, `/scope:"other-attempt"#1`), historyTask(2, task, "input:collect_user_input"), historyWait(3, task+"/attempt:1", `{"kind":"review","request_id":"r","title":"saved"}`))
	p := historyList(t, historyNew(t, historyGuardReader{r}))
	require.Equal(t, scope, p.Entries[0].Locator.Restart.ScopeID)
	require.Equal(t, HistoryRestartContaining, p.Entries[0].Locator.Restart.Relationship)
	for _, tc := range []struct {
		name string
		a    HistoryRestartAssociation
		err  error
		want string
	}{
		{name: "exact supplied mapping", a: HistoryRestartAssociation{ScopeID: scope, Relationship: HistoryRestartExact}, want: scope},
		{name: "enclosing supplied mapping", a: HistoryRestartAssociation{ScopeID: scope, Relationship: HistoryRestartContaining}, want: scope},
		{name: "failed", err: errors.New("mapping denied")},
		{name: "absent"},
		{name: "foreign", a: HistoryRestartAssociation{ScopeID: "other-job-scope", Relationship: HistoryRestartExact}},
		{name: "wrong invocation retained scope", a: HistoryRestartAssociation{ScopeID: `/scope:"other-attempt"#1`, Relationship: HistoryRestartContaining}},
		{name: "invalid relationship", a: HistoryRestartAssociation{ScopeID: scope, Relationship: "guess"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewHistoryService(HistoryConfig{Reader: historyGuardReader{r}, MapRestart: func(_ context.Context, key jobdb.JobKey, loc HistoryLocator) (HistoryRestartAssociation, error) {
				require.Equal(t, r.run.Job.JobKey, key)
				require.Equal(t, "collection:"+task, loc.InvocationID)
				return tc.a, tc.err
			}})
			require.NoError(t, err)
			page := historyList(t, s)
			require.Equal(t, tc.want, page.Entries[0].Locator.Restart.ScopeID)
			if tc.want == "" {
				require.NotEmpty(t, page.Entries[0].Locator.Restart.UnavailableReason)
			}
			detail, err := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: page.Entries[0].EntryID, SnapshotToken: page.SnapshotToken})
			require.NoError(t, err)
			require.Contains(t, string(detail.RawForm), "saved")
			require.Equal(t, page.Entries[0].Locator.Restart, detail.Locator.Restart)
		})
	}
}

func TestHistoryRestartCopiedPrefix(t *testing.T) {
	scope := `/scope:"$job"#1/attempt:1/scope:"review"#1`
	id := scope + `/attempt:1/task:"generate"#1/attempt:1`
	source := jobdb.JobKey{TenantId: "tenant", JobId: "source"}
	restart := historyRecord(10, "restart", scope)
	restart.Record.Source = &source
	r := historyFixture(historyScopeRecord(4, scope), historyRecord(5, "result", id), restart)
	r.run.Job.JobKey.JobId = "copied"
	n := int64(5)
	loc := HistoryLocator{InvocationID: id, GenerationOrdinal: &n}
	require.Equal(t, scope, RecordedHistoryRestart(r.run.Job.JobKey, r.run, loc).ScopeID)
	// A source pointer cannot stand in for an absent copied boundary.
	r.run.Execution = r.run.Execution[1:]
	a := RecordedHistoryRestart(r.run.Job.JobKey, r.run, loc)
	require.Empty(t, a.ScopeID)
	require.NotEmpty(t, a.UnavailableReason)
}

func TestHistoryRestartFailureKeepsProducerDocument(t *testing.T) {
	key := jobdb.ArtifactKey{JobId: "original", TaskOrdinal: 8, Name: "saved.md", SizeBytes: 5}
	ref, err := json.Marshal(artifacts.NewStoredRef(key))
	require.NoError(t, err)
	form := `{"kind":"review","request_id":"r","documents":{"saved":` + string(ref) + `}}`
	r := historyFixture(historyTask(1, "/task:collect", "input:collect_user_input"), historyWait(2, "/task:collect/attempt:1", form))
	r.artifact = func(tenant string, actual jobdb.ArtifactKey) jobdb.Artifact {
		require.Equal(t, "tenant", tenant)
		require.Equal(t, key, actual)
		return jobdb.NewArtifactFromBytes(key.Name, []byte("saved"))
	}
	s, err := NewHistoryService(HistoryConfig{Reader: r, MapRestart: func(context.Context, jobdb.JobKey, HistoryLocator) (HistoryRestartAssociation, error) {
		return HistoryRestartAssociation{}, errors.New("mapping unavailable")
	}})
	require.NoError(t, err)
	page := historyList(t, s)
	require.Contains(t, page.Entries[0].Locator.Restart.UnavailableReason, "mapping unavailable")
	detail, err := s.GetJobInputStoryEntry(context.Background(), GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: "job", EntryID: page.Entries[0].EntryID, SnapshotToken: page.SnapshotToken})
	require.NoError(t, err)
	require.Len(t, detail.RequestManifest, 1)
	doc, err := s.OpenJobInputStoryArtifact(context.Background(), OpenJobInputStoryArtifactRequest{TenantID: "tenant", JobID: "job", EntryID: detail.EntryID, ArtifactID: detail.RequestManifest[0].ArtifactID, SnapshotToken: page.SnapshotToken})
	require.NoError(t, err)
	data, err := io.ReadAll(doc)
	require.NoError(t, err)
	require.NoError(t, doc.Close())
	require.Equal(t, "saved", string(data))
}

func TestHistoryCompactOccurrencesKeepOrderAndTargets(t *testing.T) {
	first := `/scope:"$job"#1/attempt:1/scope:"repeat"#1/attempt:1/scope:"input"#1`
	second := `/scope:"$job"#1/attempt:1/scope:"repeat"#1/attempt:2/scope:"input"#1`
	records := func(scope string) []jobdb.ExecutionHistoryEntry {
		task := scope + `/attempt:1/task:"input:generate_form"#1`
		return []jobdb.ExecutionHistoryEntry{
			historyScopeRecord(7, scope), historyTask(7, task, "input:generate_form"),
			historyResult(7, task+"/attempt:1", `{}`, `{"question":"saved","type":"short_answer"}`),
		}
	}
	// Physical position and arrival order cannot choose the logical restart scope.
	entries := append(records(second), records(first)...)
	r := historyFixture(entries...)
	s := historyNew(t, r)
	p := historyList(t, s)
	require.Len(t, p.Entries, 2)
	require.NotEqual(t, p.Entries[0].EntryID, p.Entries[1].EntryID)
	require.Equal(t, first, p.Entries[0].Locator.Restart.ScopeID)
	require.Equal(t, second, p.Entries[1].Locator.Restart.ScopeID)
	r.run.Execution = append(records(first), records(second)...)
	fresh := historyList(t, s)
	require.Equal(t, p.Entries, fresh.Entries, "recorded identity ordering is deterministic at a shared ordinal")
	one, err := s.ListJobInputStory(context.Background(), ListJobInputStoryRequest{TenantID: "tenant", JobID: "job", PageSize: 1})
	require.NoError(t, err)
	two, err := s.ListJobInputStory(context.Background(), ListJobInputStoryRequest{TenantID: "tenant", JobID: "job", PageSize: 1, PageToken: one.NextPageToken})
	require.NoError(t, err)
	require.Equal(t, p.Entries[1], two.Entries[0])
}
