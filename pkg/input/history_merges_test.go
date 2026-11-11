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
	require.Len(t, frozen.Merges, 6)
	ids := map[string]bool{}
	recorded := 0
	for _, m := range frozen.Merges {
		require.False(t, ids[m.EntryID])
		ids[m.EntryID] = true
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
	require.Len(t, frozen.Events, 6)
	// Mutating a returned projection and adding retained records cannot affect it.
	frozen.Merges[0].CandidateSHA = "caller mutation"
	frozen.Events[0].Ordinal = 999
	r.mu.Lock()
	r.run.Execution = nil
	r.mu.Unlock()
	again, err := GetJobHistorySnapshot(ctx, s, GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job", SnapshotToken: page.SnapshotToken})
	require.NoError(t, err)
	require.Len(t, again.Merges, 6)
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
