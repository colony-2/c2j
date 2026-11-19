package metricshistory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

const occurrence = `/scope:"$job"#1/attempt:1/scope:"sequence:work"#1/attempt:1/task:"usage"#1`

func TestCommittedTaskRecordsUsePhysicalOrdinalAndPrefixPending(t *testing.T) {
	report := metrics.Channels{Runtime: metrics.Channel{Status: "absent"}, Producer: metrics.Channel{Status: "valid", Report: &metrics.Report{Version: 1, Mutations: []metrics.Mutation{{Descriptor: metrics.Descriptor{Name: "tokens", Kind: "counter", NumberType: "int64"}, Op: "add", Value: int64(9007199254740993)}}}}}
	envelope, e := task.NewOutputEnvelope(task.OutputKindActivityInvocationOutput, map[string]any{"output": map[string]any{"metrics": "business"}})
	require.NoError(t, e)
	envelope.Metrics = &report
	raw, e := json.Marshal(envelope)
	require.NoError(t, e)
	attempt := occurrence + "/attempt:1"
	result := jobdb.ExecutionRecord{Kind: "result", ID: attempt, Output: &jobdb.ExecutionSnapshot{Data: raw}, ErrorKind: "APP"}
	run := jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}}, Execution: []jobdb.ExecutionHistoryEntry{
		{Ordinal: 40, Record: jobdb.ExecutionRecord{Kind: "task", ID: occurrence}},
		{Ordinal: 41, Record: jobdb.ExecutionRecord{Kind: "attempt", ID: attempt}},
		{Ordinal: 46, Record: result},
		{Ordinal: 46, Record: result},
		{Ordinal: 47, Record: jobdb.ExecutionRecord{Kind: "result", ID: `/scope:"$job"#1/attempt:1`, Output: &jobdb.ExecutionSnapshot{Data: raw}}},
	}}
	cutoff := int64(45)
	before, e := metrics.Reduce(Records(run), metrics.Query{ThroughOrdinal: &cutoff, Deltas: true}, 47)
	require.NoError(t, e)
	require.Empty(t, before.Snapshot.Series)
	require.Equal(t, 1, before.Snapshot.Coverage.Reports["producer"]["pending"])
	after, e := metrics.Reduce(Records(run), metrics.Query{Deltas: true}, 47)
	require.NoError(t, e)
	require.Equal(t, int64(9007199254740993), after.Snapshot.Series["tokens"][0].Value)
	require.Len(t, after.Reports, 2)
	require.Equal(t, int64(46), after.Reports[1].Ordinal)
	require.Equal(t, "complete", after.Snapshot.Coverage.History)
}

type readFailure struct {
	jobdb.ConsumerRuntime
	chapters []jobdb.Chapter
	failure  error
}

func (r readFailure) ListChapters(context.Context, jobdb.ListChaptersRequest) ([]jobdb.Chapter, error) {
	return r.chapters, r.failure
}
func TestReadFailureAndIncompleteRangesCannotBecomeCompleteZero(t *testing.T) {
	for _, runtime := range []readFailure{{failure: errors.New("failed read")}, {chapters: []jobdb.Chapter{{Ordinal: 0}, {Ordinal: 2}}}, {}} {
		result, e := Read(context.Background(), runtime, jobdb.JobKey{TenantId: "tenant", JobId: "job"}, metrics.Query{})
		require.Error(t, e)
		require.Equal(t, "unavailable", result.Snapshot.Coverage.History)
		require.NotEmpty(t, result.Snapshot.Coverage.Diagnostics)
	}
}
