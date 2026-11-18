package metrics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIncrementalAdmissionMatchesHistory(t *testing.T) {
	a := NewAccumulator()
	var records []Record
	report := func(n int64, kind string, ordinal int64) Record {
		return Record{Tenant: "t", Job: "j", Task: `/scope:"$job"#1/attempt:1/task:"a"#` + string(rune('0'+ordinal)), Attempt: 1, Ordinal: ordinal, Channel: "producer", ChannelReport: Channel{Status: "valid", Report: &Report{Version: Version, Mutations: []Mutation{{Descriptor: Descriptor{Name: "tokens", Kind: kind, NumberType: "int64"}, Op: map[string]string{"counter": "add", "gauge": "set"}[kind], Value: n}}}}}
	}
	consume := func(r Record) {
		records = append(records, r)
		a.Consume(r)
		want, err := Reduce(records, Query{}, r.Ordinal)
		require.NoError(t, err)
		require.Equal(t, want.Snapshot, a.Job().Snapshot())
	}
	consume(report(9007199254740993, "counter", 1))
	scope, close := a.EnterScope()
	consume(report(100, "gauge", 2)) // Rejected globally, even in an empty local scope.
	require.Empty(t, scope.Snapshot().Series)
	consume(report(7, "counter", 3))
	require.Equal(t, int64(7), scope.Snapshot().Series["tokens"][0].Value)
	snapshot := scope.Snapshot()
	snapshot.Series["tokens"][0].Value = int64(999)
	snapshot.Coverage.Reports["producer"]["valid"] = 999
	require.Equal(t, int64(7), scope.Snapshot().Series["tokens"][0].Value)
	close()
	consume(report(2, "counter", 4))
	require.Equal(t, int64(7), scope.Snapshot().Series["tokens"][0].Value)
	// Duplicate notification cannot become a new report in a newly entered scope.
	later, done := a.EnterScope()
	defer done()
	a.Consume(records[0])
	require.Empty(t, later.Snapshot().Series)
}

func TestIncrementalSnapshotsDetachNestedValues(t *testing.T) {
	a := NewAccumulator()
	a.Consume(Record{Task: "a", Attempt: 1, Ordinal: 3, Channel: "runtime", ChannelReport: Channel{
		Status: "valid", Phases: map[string]string{"execution": "succeeded"},
		CPU: &CPUCoverage{Availability: "available", MeasuredSeconds: 2, ProcessSeconds: 1, ExcludedSeconds: map[string]float64{"concurrent_tasks": 4}},
		Report: &Report{Version: Version, Mutations: []Mutation{
			{Descriptor: Descriptor{Name: "latency", Kind: "histogram", NumberType: "float64", Bounds: []float64{2}}, Labels: map[string]string{"model": "a"}, Op: "observe", Value: float64(1)},
			{Descriptor: Descriptor{Name: "level", Kind: "gauge", NumberType: "int64"}, Op: "set", Value: int64(7)},
		}},
	}})
	want := a.Job().Snapshot()
	require.Len(t, want.Series, 2)
	changed := a.Job().Snapshot()
	changed.Series["latency"][0].Labels["model"] = "changed"
	changed.Series["latency"][0].Bounds[0] = 99
	changed.Series["latency"][0].Histogram.BucketCounts[0] = 99
	changed.Series["level"][0].Position.Ordinal = 99
	changed.Coverage.Phases["execution"]["succeeded"] = 99
	changed.Coverage.CPU.ExcludedSeconds["concurrent_tasks"] = 99
	require.Equal(t, want, a.Job().Snapshot())
	// Future reports also cannot mutate an earlier snapshot's histogram population.
	a.Consume(Record{Task: "b", Attempt: 1, Ordinal: 4, Channel: "runtime", ChannelReport: Channel{Status: "valid", Report: &Report{Version: Version, Mutations: []Mutation{
		{Descriptor: Descriptor{Name: "latency", Kind: "histogram", NumberType: "float64", Bounds: []float64{2}}, Labels: map[string]string{"model": "a"}, Op: "observe", Value: float64(3)},
	}}}})
	require.Equal(t, int64(1), want.Series["latency"][0].Histogram.Count)
	require.Equal(t, int64(2), a.Job().Snapshot().Series["latency"][0].Histogram.Count)
}
