package input

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

const mergeEvidence = `{"candidate_sha":"abc","candidate_subject":"actual","publication_branch":"refs/heads/main","publication_disposition":"published"}`

func TestHistoryMergeIdentitiesAndEvidence(t *testing.T) {
	ctx := context.Background()
	prefix := `/scope:"$job"#1/attempt:1/scope:"loop"#1/attempt:1/`
	records := []jobdb.ExecutionHistoryEntry{}
	for i, tc := range []struct {
		id, typ, out string
		failed       bool
	}{
		{prefix + `task:"merge:merge"#1`, "merge:merge", mergeEvidence, false},
		{prefix + `task:"merge:merge"#1`, "merge:merge", mergeEvidence, false}, // distinct accepted retry
		{prefix + `task:"squashrebasemerge:merge"#2`, "squashrebasemerge:merge", mergeEvidence, false},
		{strings.Replace(prefix, `"loop"#1`, `"loop"#2`, 1) + `task:"merge:merge"#1`, "merge:merge", mergeEvidence, false},
		{strings.Replace(prefix, "/attempt:1/", "/attempt:2/", 1) + `task:"merge:merge"#1`, "merge:merge", mergeEvidence, false},
		{prefix + `task:"squashrebasemerge:squash"#2`, "squashrebasemerge:squash", mergeEvidence, false},
		{prefix + `task:"unrelated"#1`, "something:merge", mergeEvidence, false},
		{prefix + `task:"historical"#1`, "merge:merge", `{"merged_hash":"abc","target_branch":"main"}`, false},
		{prefix + `task:"failed"#1`, "merge:merge", mergeEvidence, true},
	} {
		records = append(records, historyTask(1, tc.id, tc.typ))
		result := historyResult(10, tc.id+"/attempt:"+string(rune('1'+i)), "{}", tc.out)
		if tc.failed {
			result.Record.ErrorKind = "SystemError"
		}
		records = append(records, result)
	}
	// A successful accepted result without retained output remains unavailable.
	missing := historyResult(10, prefix+`task:"missing-output"#1/attempt:1`, "{}", mergeEvidence)
	missing.Record.Output = nil
	records = append(records, historyTask(1, prefix+`task:"missing-output"#1`, "merge:merge"), missing)
	// A declared but unaccepted execution must not be reconstructed.
	records = append(records, historyTask(11, prefix+`task:"lost"#1`, "merge:merge"))
	child := historyRecord(12, "join_result", "child-job-result")
	child.Record.Output = historyOut(mergeEvidence)
	records = append(records, child)
	r := historyFixture(records...)
	s := historyNew(t, r)
	page := historyList(t, s)
	require.Empty(t, page.Entries)
	require.NotEmpty(t, page.SnapshotToken)
	frozen, err := GetJobHistorySnapshot(ctx, s, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job", SnapshotToken: page.SnapshotToken})
	require.NoError(t, err)
	require.Len(t, frozen.Merges, 7)
	ids := map[string]bool{}
	recorded := 0
	for _, m := range frozen.Merges {
		require.False(t, ids[m.EntryID])
		ids[m.EntryID] = true
		require.EqualValues(t, 10, m.ResultOrdinal)
		require.Contains(t, m.InvocationID, "/attempt:")
		require.Positive(t, m.TaskAttempt)
		if i := len(ids) - 1; i > 0 {
			require.Less(t, frozen.Merges[i-1].InvocationID, m.InvocationID)
		}
		require.Nil(t, m.CompletedAt)
		require.Contains(t, []int{1, 2}, m.JobAttempt)
		if m.EvidenceAvailability == "recorded" {
			recorded++
			require.Equal(t, "actual", *m.CandidateSubject)
		} else {
			require.Empty(t, m.PublicationDisposition)
			require.Nil(t, m.CandidateSubject)
		}
	}
	require.Equal(t, 5, recorded)
	require.Len(t, frozen.Events, 7)
	// Mutating a returned projection and adding retained records cannot affect it.
	frozen.Merges[0].CandidateSHA = "caller mutation"
	frozen.Events[0].Ordinal = 999
	r.mu.Lock()
	r.run.Execution = nil
	r.mu.Unlock()
	again, err := GetJobHistorySnapshot(ctx, s, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job", SnapshotToken: page.SnapshotToken})
	require.NoError(t, err)
	require.Len(t, again.Merges, 7)
	require.NotEqual(t, "caller mutation", again.Merges[0].CandidateSHA)
	require.EqualValues(t, 10, again.Events[0].Ordinal)
	live, err := GetJobHistorySnapshot(ctx, s, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job"})
	require.NoError(t, err)
	require.Empty(t, live.Merges)
}

func TestHistorySharedSnapshotFailures(t *testing.T) {
	ctx := context.Background()
	r := historyFixture()
	s, err := NewHistoryService(HistoryConfig{Reader: r, MaxSnapshots: 1, SnapshotTTL: time.Hour})
	require.NoError(t, err)
	first, err := GetJobHistorySnapshot(ctx, s, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job"})
	require.NoError(t, err)
	for _, req := range []GetJobHistorySnapshotRequest{
		{TenantID: "tenant", JobID: "other", SnapshotToken: first.SnapshotToken},
		{TenantID: "tenant", JobID: "job", SnapshotToken: "invalid"},
	} {
		_, err = GetJobHistorySnapshot(ctx, s, req)
		require.ErrorIs(t, err, ErrHistoryInvalidCursor)
	}
	_, err = GetJobHistorySnapshot(ctx, s, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job"})
	require.NoError(t, err)
	_, err = GetJobHistorySnapshot(ctx, s, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job", SnapshotToken: first.SnapshotToken})
	require.ErrorIs(t, err, ErrHistorySnapshotExpired)
	internal := s.(*historyService)
	internal.mu.Lock()
	for _, snap := range internal.snapshots {
		snap.created = time.Now().Add(-2 * time.Hour)
	}
	internal.mu.Unlock()
	// Expiration never replaces the observation, even if the reader would fail.
	r.err = jobdb.ErrJobNotFound
	p := historyCursor{}
	for id, snap := range internal.snapshots {
		p = historyCursor{Version: 1, Tenant: "tenant", Job: "job", Snapshot: id, Revision: snap.highWater}
	}
	_, err = GetJobHistorySnapshot(ctx, s, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job", SnapshotToken: internal.historyToken(p)})
	require.ErrorIs(t, err, ErrHistorySnapshotExpired)
}

func TestHistoryEventsUseSeparateRetainedOrdinals(t *testing.T) {
	generation, acceptance := int64(5), int64(12)
	collection := `/scope:"$job"#1/attempt:2/task:"input:collect_user_input"#1`
	result := historyRecord(12, "result", collection+"/attempt:3")
	entries := []JobInputStoryEntryDetail{{JobInputStoryEntry: JobInputStoryEntry{EntryID: "input", Locator: HistoryLocator{InvocationID: "generation/attempt:1", GenerationOrdinal: &generation, AcceptedOrdinal: &acceptance, CollectionInvocationIDs: []string{collection}}}}}
	merges := []HistoryMergeEntry{{EntryID: "merge-b", ResultOrdinal: 12, InvocationID: "same"}, {EntryID: "merge-a", ResultOrdinal: 12, InvocationID: "same"}}
	events := historyEvents(entries, merges, jobdb.GetJobRunResponse{Execution: []jobdb.ExecutionHistoryEntry{result}})
	require.Len(t, events, 4)
	require.Equal(t, HistoryEvent{Ordinal: 5, InvocationID: "generation/attempt:1", Kind: "request_generation", EntryID: "input"}, events[0])
	require.Equal(t, HistoryEvent{Ordinal: 12, InvocationID: collection + "/attempt:3", Kind: "response_acceptance", EntryID: "input"}, events[1])
	require.Equal(t, "merge-a", events[2].EntryID)
	require.Equal(t, "merge-b", events[3].EntryID)
}

func TestHistoryMergeEvidenceDispositionsAndCoordinates(t *testing.T) {
	id := `/scope:"$job"#1/attempt:2/task:"merge:merge"#1`
	at := time.Date(2026, 11, 11, 1, 2, 3, 0, time.UTC)
	for _, op := range []string{"merge:merge", "squashrebasemerge:merge"} {
		for _, disposition := range []string{"published", "already_present", "no_changes"} {
			for _, subject := range []struct {
				name, field string
				want        *string
			}{
				{"absent", "", nil}, {"null", `,"candidate_subject":null`, nil},
				{"empty", `,"candidate_subject":""`, ptrHistorySubject("")},
				{"nonempty", `,"candidate_subject":" exact subject "`, ptrHistorySubject(" exact subject ")},
			} {
				t.Run(op+"/"+disposition+"/"+subject.name, func(t *testing.T) {
					output := `{"candidate_sha":"abc","publication_branch":"refs/heads/main","publication_disposition":"` + disposition + `"` + subject.field + `}`
					result := historyResult(42, id+"/attempt:3", "{}", output)
					result.Record.At = at
					reader := historyFixture(historyTask(1, id, op), result)
					projected := historyMergeProjection(reader.run.Job.JobKey, reader.run)
					service := historyNew(t, reader)
					snapshot, err := GetJobHistorySnapshot(context.Background(), service, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job"})
					require.NoError(t, err)
					require.Equal(t, projected, snapshot.Merges)
					require.Len(t, projected, 1)
					m := projected[0]
					require.Equal(t, "recorded", m.EvidenceAvailability)
					require.Equal(t, subject.want, m.CandidateSubject)
					require.Equal(t, output, string(m.Output))
					require.Equal(t, "abc", m.CandidateSHA)
					require.Equal(t, "refs/heads/main", m.PublicationBranch)
					require.Equal(t, disposition, m.PublicationDisposition)
					require.Equal(t, op, m.Operation)
					require.Equal(t, "tenant", m.TenantID)
					require.Equal(t, "job", m.JobID)
					require.Equal(t, 2, m.JobAttempt)
					require.Equal(t, 3, m.TaskAttempt)
					require.Equal(t, id+"/attempt:3", m.InvocationID)
					require.EqualValues(t, 42, m.ResultOrdinal)
					require.Equal(t, &at, m.CompletedAt)
					require.NotEmpty(t, m.EntryID)
					verified := m.EvidenceAvailability == "recorded" && (m.PublicationDisposition == "published" || m.PublicationDisposition == "already_present")
					require.Equal(t, disposition != "no_changes", verified)
					_, err = GetJobHistorySnapshot(context.Background(), service, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "child"})
					require.ErrorIs(t, err, jobdb.ErrJobNotFound)
				})
			}
		}
	}
}

func ptrHistorySubject(s string) *string { return &s }

func TestHistoryMergeMalformedEvidence(t *testing.T) {
	id := `/scope:"$job"#1/attempt:1/task:"merge"#1`
	for _, tc := range []struct{ name, output string }{
		{"missing sha", strings.Replace(mergeEvidence, `"candidate_sha":"abc",`, "", 1)},
		{"empty sha", strings.Replace(mergeEvidence, `"abc"`, `""`, 1)},
		{"missing branch", strings.Replace(mergeEvidence, `"publication_branch":"refs/heads/main",`, "", 1)},
		{"empty branch", strings.Replace(mergeEvidence, `"refs/heads/main"`, `""`, 1)},
		{"missing disposition", strings.Replace(mergeEvidence, `,"publication_disposition":"published"`, "", 1)},
		{"unsupported disposition", strings.Replace(mergeEvidence, "published", "future", 1)},
		{"wrong subject type", strings.Replace(mergeEvidence, `"actual"`, "42", 1)},
		{"wrong sha type", strings.Replace(mergeEvidence, `"abc"`, "42", 1)},
		{"wrong branch type", strings.Replace(mergeEvidence, `"refs/heads/main"`, "42", 1)},
		{"wrong disposition type", strings.Replace(mergeEvidence, `"published"`, "42", 1)},
		{"malformed json", `{`}, {"array", `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := historyMergeProjection(jobdb.JobKey{}, jobdb.GetJobRunResponse{Execution: []jobdb.ExecutionHistoryEntry{historyTask(1, id, "merge:merge"), historyResult(2, id+"/attempt:1", "{}", tc.output)}})
			require.Len(t, got, 1)
			require.Equal(t, "unavailable", got[0].EvidenceAvailability)
		})
	}
	for _, envelope := range []string{`{"v":2,"kind":"activity_output","payload":{"output":` + mergeEvidence + `}}`, `{"v":1,"kind":"other","payload":{"output":` + mergeEvidence + `}}`, `{}`} {
		result := historyResult(2, id+"/attempt:1", "{}", mergeEvidence)
		result.Record.Output.Data = historyRaw(envelope)
		got := historyMergeProjection(jobdb.JobKey{}, jobdb.GetJobRunResponse{Execution: []jobdb.ExecutionHistoryEntry{historyTask(1, id, "merge:merge"), result}})
		require.Len(t, got, 1)
		require.Equal(t, "unavailable", got[0].EvidenceAvailability)
	}
}

func TestHistorySharedSnapshotOtherInstance(t *testing.T) {
	ctx := context.Background()
	r := historyFixture()
	first := historyNew(t, r)
	second := historyNew(t, r)
	snapshot, err := GetJobHistorySnapshot(ctx, first, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job"})
	require.NoError(t, err)
	_, err = GetJobHistorySnapshot(ctx, second, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job", SnapshotToken: snapshot.SnapshotToken})
	require.ErrorIs(t, err, ErrHistorySnapshotExpired)
}
