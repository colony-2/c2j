package metrics

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const scopeA = `/scope:"$job"#1/attempt:1/scope:"sequence:a/b"#1`
const scopeB = `/scope:"$job"#1/attempt:1/scope:"sequence:a/bc"#1`

func counter(n int64) Mutation {
	return Mutation{Descriptor: Descriptor{Name: "tokens_total", Kind: "counter", NumberType: "int64", Unit: "{token}"}, Labels: map[string]string{"model": "demo"}, Op: "add", Value: n}
}
func report(ms ...Mutation) Channel {
	return Channel{Status: "valid", Report: &Report{Version: Version, Mutations: ms}}
}
func record(ord int64, ch Channel) Record {
	return Record{Tenant: "t", Job: "j", Task: scopeA + `/attempt:1/task:"run"#1`, Attempt: 1, Ordinal: ord, Channel: "producer", ChannelReport: ch}
}
func second(r Record) Record { r.Task = scopeB + `/attempt:1/task:"run"#1`; r.Ordinal++; return r }
func reduce(t *testing.T, rs []Record, q Query) Result {
	t.Helper()
	r, e := Reduce(rs, q, 1000)
	require.NoError(t, e)
	return r
}
func histogram(n float64) Mutation {
	return Mutation{Descriptor: Descriptor{Name: "latency", Kind: "histogram", NumberType: "float64", Unit: "s"}, Op: "observe", Value: n}
}
func TestExactCountersAndLabelIdentity(t *testing.T) {
	raw := []byte(`{"version":1,"mutations":[{"name":"tokens_total","kind":"counter","number_type":"int64","unit":"{token}","labels":{"model":"demo"},"op":"add","value":9007199254740993}]}`)
	decoded, diag := DecodeReport(raw)
	require.Empty(t, diag)
	first := record(1, Channel{Status: "valid", Report: decoded})
	next := second(record(2, report(counter(2))))
	out := reduce(t, []Record{first, next}, Query{})
	require.Equal(t, int64(9007199254740995), out.Snapshot.Series["tokens_total"][0].Value)
	encoded, e := json.Marshal(out)
	require.NoError(t, e)
	var roundtrip Result
	require.NoError(t, json.Unmarshal(encoded, &roundtrip))
	require.Equal(t, out.Snapshot.Series, roundtrip.Snapshot.Series)
	a, b := counter(1), counter(1)
	a.Labels = map[string]string{"a": "x", "b": "y"}
	b.Labels = map[string]string{"b": "y", "a": "x"}
	c, d := counter(1), counter(1)
	c.Labels = nil
	d.Labels = map[string]string{"a": ""}
	out = reduce(t, []Record{record(1, report(a, b, c, d))}, Query{})
	require.Len(t, out.Snapshot.Series["tokens_total"], 3)
	require.NotEqual(t, seriesKey(c), seriesKey(d))
}
func TestGaugeAndHistogramSelectedHistory(t *testing.T) {
	gauge := counter(0)
	gauge.Name = "queue"
	gauge.Kind = "gauge"
	gauge.Op = "set"
	gauge.Value = int64(8)
	first := record(1, report(gauge, histogram(.2), histogram(1.2)))
	gauge.Value = int64(3)
	next := second(record(2, report(gauge, histogram(2))))
	out := reduce(t, []Record{next, first}, Query{})
	h := out.Snapshot.Series["latency"][0].Histogram
	require.Equal(t, int64(3), h.Count)
	require.InDelta(t, 3.4, h.Sum, 1e-12)
	require.Equal(t, .2, h.Min)
	require.Equal(t, 2., h.Max)
	require.InDelta(t, 3.4/3, Float(h.Sum)/float64(h.Count), 1e-12)
	require.Equal(t, int64(3), out.Snapshot.Series["queue"][0].Value)
	cutoff := int64(1)
	out = reduce(t, []Record{first, next}, Query{ThroughOrdinal: &cutoff})
	require.Equal(t, int64(8), out.Snapshot.Series["queue"][0].Value)
	require.Equal(t, 1.2, out.Snapshot.Series["latency"][0].Max)
	out = reduce(t, []Record{first, next}, Query{Scope: scopeB})
	require.Equal(t, int64(1), out.Snapshot.Series["latency"][0].Count)
	require.Equal(t, 2., out.Snapshot.Series["latency"][0].Min)
	// Fixed channel order, then mutation order, resolves tied result ordinals.
	runtime := first
	runtime.Channel = "runtime"
	runtime.ChannelReport = report(gauge)
	runtime.ChannelReport.Report.Mutations[0].Value = int64(99)
	out = reduce(t, []Record{first, runtime}, Query{})
	require.Equal(t, int64(8), out.Snapshot.Series["queue"][0].Value)
}
func TestHistogramBucketsAndCompactEquivalence(t *testing.T) {
	var mutations []Mutation
	for _, n := range []float64{-2, 0, .5, 1, 2} {
		m := histogram(n)
		m.Bounds = []float64{0, 1}
		mutations = append(mutations, m)
	}
	out := reduce(t, []Record{record(1, report(mutations...))}, Query{})
	h := out.Snapshot.Series["latency"][0].Histogram
	require.Equal(t, []int64{2, 2, 1}, h.BucketCounts)
	compact := mutations[0]
	compact.Op = "merge"
	compact.Value = nil
	compact.Delta = h
	merged := reduce(t, []Record{record(1, report(compact))}, Query{})
	require.Equal(t, h, merged.Snapshot.Series["latency"][0].Histogram)
	empty := compact
	empty.Delta = &Histogram{Count: 0, Sum: 0., BucketCounts: []int64{0, 0, 0}}
	emptyView := reduce(t, []Record{record(1, report(empty))}, Query{})
	require.Nil(t, emptyView.Snapshot.Series["latency"][0].Min)
}
func TestAtomicAdmissionPrecedesFiltering(t *testing.T) {
	first := record(1, report(counter(4)))
	conflict := counter(2)
	conflict.Kind = "gauge"
	conflict.Op = "set"
	later := second(record(2, report(conflict)))
	out := reduce(t, []Record{first, later}, Query{Scope: scopeB, Deltas: true})
	require.Empty(t, out.Snapshot.Series)
	require.Equal(t, "invalid", out.Reports[0].ChannelReport.Status)
	overflow := counter(math.MaxInt64)
	initial := record(1, report(overflow))
	tentative := counter(2)
	tentative.Name = "tentative"
	bad := second(record(2, report(tentative, counter(1))))
	good := second(bad)
	good.Task += `/attempt:1/task:"third"#1`
	good.Ordinal = 3
	tentative.Kind = "gauge"
	tentative.Op = "set"
	good.ChannelReport = report(tentative)
	out = reduce(t, []Record{initial, bad, good}, Query{Deltas: true})
	require.Equal(t, "invalid", out.Reports[1].ChannelReport.Status)
	require.Equal(t, int64(2), out.Snapshot.Series["tentative"][0].Value)
	require.Equal(t, int64(math.MaxInt64), out.Snapshot.Series["tokens_total"][0].Value)
	// An invalid producer report leaves the runtime channel eligible.
	runtime := initial
	runtime.Channel = "runtime"
	m := histogram(1)
	m.Name = "c2j_task_duration_seconds"
	runtime.ChannelReport = report(m)
	bad = initial
	negative := counter(-1)
	bad.ChannelReport = report(counter(4), negative)
	out = reduce(t, []Record{runtime, bad}, Query{})
	require.Contains(t, out.Snapshot.Series, "c2j_task_duration_seconds")
	require.NotContains(t, out.Snapshot.Series, "tokens_total")
}
func TestDuplicatesAndRawDeltaOperations(t *testing.T) {
	r := record(1, report(histogram(2)))
	copy := r
	copy.Ordinal = 3
	out := reduce(t, []Record{r, copy}, Query{Deltas: true})
	require.Len(t, out.Reports, 1)
	require.Equal(t, int64(1), out.Snapshot.Series["latency"][0].Count)
	require.Equal(t, "observe", out.Reports[0].ChannelReport.Report.Mutations[0].Op)
	copy.ChannelReport = report(histogram(3))
	out = reduce(t, []Record{r, copy}, Query{})
	require.Equal(t, "partial", out.Snapshot.Coverage.History)
}
func TestReportValidation(t *testing.T) {
	cases := map[string]func(*Mutation){
		"negative counter": func(m *Mutation) { m.Value = int64(-1) },
		"float integer":    func(m *Mutation) { m.Value = 1.5 },
		"reserved":         func(m *Mutation) { m.Name = "c2j_private" },
		"nonfinite":        func(m *Mutation) { m.NumberType = "float64"; m.Value = math.Inf(1) },
		"name bytes":       func(m *Mutation) { m.Name = strings.Repeat("é", 65) },
		"label bytes":      func(m *Mutation) { m.Labels = map[string]string{"label": strings.Repeat("é", 129)} },
		"unit":             func(m *Mutation) { m.Unit = strings.Repeat("x", 129) },
		"label count": func(m *Mutation) {
			m.Labels = map[string]string{}
			for i := 0; i <= MaxLabels; i++ {
				m.Labels[strconv.Itoa(i)] = ""
			}
		},
		"scalar delta": func(m *Mutation) { m.Delta = &Histogram{} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			m := counter(1)
			change(&m)
			_, e := Validate(&Report{Version: Version, Mutations: []Mutation{counter(1), m}}, false)
			require.Error(t, e)
		})
	}
	for _, raw := range []string{
		`{"version":1,"identity":"forged","mutations":[]}`,
		`{"version":1,"channel":"runtime","mutations":[]}`,
		`{"version":1,"mutations":[]} {}`,
		`{"version":2,"mutations":[]}`,
	} {
		r, diag := DecodeReport([]byte(raw))
		require.Nil(t, r)
		require.NotEmpty(t, diag)
	}
	m := counter(1)
	m.Name = strings.Repeat("é", 64)
	m.Labels = map[string]string{strings.Repeat("k", 128): strings.Repeat("é", 128)}
	_, e := Validate(&Report{Version: Version, Mutations: []Mutation{m}}, false)
	require.NoError(t, e)
}
func TestHistogramRejectsInvalidDeltasAndOverflow(t *testing.T) {
	for _, h := range []*Histogram{
		{Count: 1, Sum: 1., Min: 1., Max: 1., BucketCounts: []int64{}},
		{Count: 2, Sum: 1., Min: 1., Max: 1., BucketCounts: []int64{1}},
		{Count: 1, Sum: 1., Min: 2., Max: 1., BucketCounts: []int64{1}},
		{Count: 0, Sum: 0., Min: 0., BucketCounts: []int64{0}},
		{Count: 1, Sum: 2., Min: 1., Max: 1., BucketCounts: []int64{1}},
	} {
		m := histogram(0)
		m.Value = nil
		m.Op = "merge"
		m.Delta = h
		_, e := Validate(&Report{Version: Version, Mutations: []Mutation{m}}, false)
		require.Error(t, e)
	}
	for _, bounds := range [][]float64{{1, 1}, {2, 1}, {math.NaN()}, {math.Inf(1)}, make([]float64, 33)} {
		m := histogram(0)
		m.Bounds = bounds
		_, e := Validate(&Report{Version: Version, Mutations: []Mutation{m}}, false)
		require.Error(t, e)
	}
	_, diag := DecodeReport([]byte(`{"version":1,"mutations":[{"name":"h","kind":"histogram","number_type":"float64","op":"merge","delta":{"count":1.5,"sum":0,"bucket_counts":[1]}}]}`))
	require.NotEmpty(t, diag)
	m := histogram(0)
	m.Op = "merge"
	m.Value = nil
	m.Delta = &Histogram{Count: math.MaxInt64, Sum: 0., Min: 0., Max: 0., BucketCounts: []int64{math.MaxInt64}}
	first := record(1, report(m))
	next := second(record(2, report(histogram(0))))
	out := reduce(t, []Record{first, next}, Query{Deltas: true})
	require.Equal(t, "invalid", out.Reports[1].ChannelReport.Status)
	require.Equal(t, int64(math.MaxInt64), out.Snapshot.Series["latency"][0].Count)
	m = histogram(math.MaxFloat64)
	first = record(1, report(m))
	next = second(record(2, report(m)))
	out = reduce(t, []Record{first, next}, Query{Deltas: true})
	require.Equal(t, "invalid", out.Reports[1].ChannelReport.Status)
}
func TestSeriesLimitAndRejectedReservations(t *testing.T) {
	var records []Record
	for i := 0; i < MaxSeries/100; i++ {
		var ms []Mutation
		for j := 0; j < 100; j++ {
			m := counter(1)
			m.Labels = map[string]string{"n": strconv.Itoa(i*100 + j)}
			ms = append(ms, m)
		}
		r := record(int64(i), report(ms...))
		r.Task += `/attempt:1/task:"` + strconv.Itoa(i) + `"#1`
		records = append(records, r)
	}
	excess := second(record(101, report(counter(1))))
	records = append(records, excess)
	// Existing series remains admissible after the cardinality rejection.
	m := counter(1)
	m.Labels = map[string]string{"n": "0"}
	last := second(excess)
	last.Task += `/attempt:1/task:"last"#1`
	last.ChannelReport = report(m)
	records = append(records, last)
	out := reduce(t, records, Query{Deltas: true})
	require.Len(t, out.Snapshot.Series["tokens_total"], MaxSeries)
	require.Equal(t, "invalid", out.Reports[len(out.Reports)-2].ChannelReport.Status)
	require.Equal(t, "valid", out.Reports[len(out.Reports)-1].ChannelReport.Status)
}
func TestCollectorBoundsAndConcurrentCompaction(t *testing.T) {
	c := NewCollector(nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				c.Observe("latency", "s", nil, .5)
			}
		}()
	}
	wg.Wait()
	got := c.Snapshot()
	require.Equal(t, "valid", got.Status)
	require.Len(t, got.Report.Mutations, 1)
	require.Equal(t, int64(4000), got.Report.Mutations[0].Delta.Count)
	c = NewCollector(nil)
	for i := 0; i < MaxMutations+1; i++ {
		m := counter(1)
		m.Labels = map[string]string{"id": strconv.Itoa(i)}
		c.Emit(m)
	}
	require.Equal(t, "invalid", c.Snapshot().Status)
	require.Nil(t, c.Snapshot().Report)
	c.AddInt("other", "", nil, 1)
	require.Nil(t, c.Snapshot().Report)
}
func TestNativeSelectors(t *testing.T) {
	_, e := Segments(scopeA)
	require.NoError(t, e)
	for _, q := range []Query{{Scope: "name"}, {Task: scopeA}, {Attempt: 2}, {ThroughOrdinal: func() *int64 { n := int64(-1); return &n }()}} {
		require.Error(t, q.Validate())
	}
	r := record(1, report(counter(1)))
	r.Task = strings.Replace(r.Task, "/attempt:1/task:", "/attempt:2/task:", 1)
	require.Empty(t, reduce(t, []Record{r}, Query{Scope: scopeA, Attempt: 1}).Snapshot.Series)
	require.NotEmpty(t, reduce(t, []Record{r}, Query{Scope: scopeA, Attempt: 2}).Snapshot.Series)
}
