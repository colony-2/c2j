// Package metrics describes committed task deltas and deterministic derived views.
// It deliberately has no worker, storage, exporter or instrument registry.
package metrics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
)

const Version = 1
const ReducerVersion = 1

// JobOutputKey is reserved in recipe job output for the accumulated task totals.
// Operation outputs, including business fields named metrics, are unchanged.
const JobOutputKey = "c2j_metrics"

const MaxBytes = 64 << 10
const MaxMutations = 256
const MaxLabels = 16
const MaxNameBytes = 128
const MaxLabelBytes = 256
const MaxBounds = 32
const MaxSeries = 10000

type Descriptor struct {
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	NumberType string    `json:"number_type"`
	Unit       string    `json:"unit,omitempty"`
	Bounds     []float64 `json:"bounds,omitempty"`
}
type Histogram struct {
	Count        int64   `json:"count"`
	Sum          any     `json:"sum"`
	Min          any     `json:"min,omitempty"`
	Max          any     `json:"max,omitempty"`
	BucketCounts []int64 `json:"bucket_counts"`
}
type Mutation struct {
	Descriptor
	Labels map[string]string `json:"labels,omitempty"`
	Op     string            `json:"op"`
	Value  any               `json:"value,omitempty"`
	Delta  *Histogram        `json:"delta,omitempty"`
}
type Report struct {
	Version   int        `json:"version"`
	Mutations []Mutation `json:"mutations"`
}
type CPUCoverage struct {
	ProcessSeconds  float64            `json:"process_seconds"`
	Availability    string             `json:"availability"`
	MeasuredSeconds float64            `json:"measured_seconds"`
	ExcludedSeconds map[string]float64 `json:"excluded_seconds,omitempty"`
}
type Channel struct {
	Status     string            `json:"status"`
	Diagnostic string            `json:"diagnostic,omitempty"`
	Report     *Report           `json:"report,omitempty"`
	Phases     map[string]string `json:"phases,omitempty"`
	CPU        *CPUCoverage      `json:"cpu,omitempty"`
}
type Channels struct {
	Runtime  Channel `json:"runtime"`
	Producer Channel `json:"producer"`
}
type Record struct {
	Tenant        string  `json:"tenant"`
	Job           string  `json:"job"`
	Task          string  `json:"task"`
	Attempt       int     `json:"attempt"`
	Ordinal       int64   `json:"ordinal"`
	Channel       string  `json:"channel"`
	ChannelReport Channel `json:"report"`
}
type Position struct {
	Task     string `json:"task"`
	Attempt  int    `json:"attempt"`
	Ordinal  int64  `json:"ordinal"`
	Channel  string `json:"channel"`
	Mutation int    `json:"mutation"`
}
type Point struct {
	Descriptor
	Labels     map[string]string `json:"labels,omitempty"`
	Value      any               `json:"value,omitempty"`
	*Histogram `json:"histogram,omitempty"`
	Position   *Position `json:"position,omitempty"`
}
type Coverage struct {
	History     string                    `json:"history"`
	Reports     map[string]map[string]int `json:"reports"`
	Phases      map[string]map[string]int `json:"phases"`
	CPU         CPUCoverage               `json:"cpu"`
	Diagnostics []string                  `json:"diagnostics,omitempty"`
}
type Snapshot struct {
	Version          int                `json:"version"`
	ReducerVersion   int                `json:"reducer_version"`
	Cutoff           int64              `json:"cutoff"`
	HistoryView      string             `json:"history_view"`
	Source           string             `json:"source"`
	RestartPartition string             `json:"restart_partition"`
	Series           map[string][]Point `json:"series"`
	Coverage         Coverage           `json:"coverage"`
}
type Query struct {
	Name           string `json:"name,omitempty"`
	Scope          string `json:"scope,omitempty"`
	Task           string `json:"task,omitempty"`
	Attempt        int    `json:"attempt,omitempty"`
	ThroughOrdinal *int64 `json:"through_ordinal,omitempty"`
	Deltas         bool   `json:"deltas,omitempty"`
}
type Result struct {
	Snapshot Snapshot `json:"snapshot"`
	Reports  []Record `json:"reports,omitempty"`
}

func Empty(cutoff int64) Snapshot {
	return Snapshot{Version: Version, ReducerVersion: ReducerVersion, Cutoff: cutoff, HistoryView: "effective", Source: "computed", RestartPartition: "unavailable", Series: map[string][]Point{}, Coverage: Coverage{History: "complete", Reports: map[string]map[string]int{}, Phases: map[string]map[string]int{}, CPU: CPUCoverage{Availability: "unavailable", ExcludedSeconds: map[string]float64{}}}}
}

// DecodeReport is strict: identity, channel, and unexpected fields are not producer input.
// Errors are fixed safe text; untrusted values never become diagnostics.
func DecodeReport(raw []byte) (*Report, string) {
	if len(raw) > MaxBytes {
		return nil, "metric report exceeds byte limit"
	}
	var r Report
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		return nil, "invalid metric report shape"
	}
	if r.Version != Version {
		return nil, "unsupported metric report version"
	}
	return &r, ""
}

func number(v any, typ string) (any, error) {
	if v == nil {
		return nil, fmt.Errorf("missing metric number")
	}
	if typ == "int64" {
		switch n := v.(type) {
		case int64:
			return n, nil
		case int:
			return int64(n), nil
		case json.Number:
			i, e := strconv.ParseInt(string(n), 10, 64)
			if e == nil {
				return i, nil
			}
		}
		return nil, fmt.Errorf("metric requires exact int64")
	}
	if typ != "float64" {
		return nil, fmt.Errorf("invalid metric numeric type")
	}
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case int64:
		f = float64(n)
	case int:
		f = float64(n)
	case json.Number:
		var e error
		f, e = n.Float64()
		if e != nil {
			return nil, fmt.Errorf("invalid metric number")
		}
	default:
		return nil, fmt.Errorf("invalid metric number")
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("nonfinite metric number")
	}
	return f, nil
}
func zero(typ string) any {
	if typ == "int64" {
		return int64(0)
	}
	return float64(0)
}
func negative(v any) bool {
	if n, ok := v.(int64); ok {
		return n < 0
	}
	return v.(float64) < 0
}
func compare(a, b any) int {
	if n, ok := a.(int64); ok {
		v := b.(int64)
		if n < v {
			return -1
		}
		if n > v {
			return 1
		}
		return 0
	}
	n, v := a.(float64), b.(float64)
	if n < v {
		return -1
	}
	if n > v {
		return 1
	}
	return 0
}
func add(a, b any) (any, error) {
	if n, ok := a.(int64); ok {
		v := b.(int64)
		if (v > 0 && n > math.MaxInt64-v) || (v < 0 && n < math.MinInt64-v) {
			return nil, fmt.Errorf("metric integer overflow")
		}
		return n + v, nil
	}
	n := a.(float64) + b.(float64)
	if math.IsInf(n, 0) || math.IsNaN(n) {
		return nil, fmt.Errorf("metric float overflow")
	}
	return n, nil
}
func addCount(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, fmt.Errorf("metric count overflow")
	}
	return a + b, nil
}
func Float(v any) float64 {
	switch n := v.(type) {
	case int64:
		return float64(n)
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

// UnmarshalJSON keeps generic numeric fields exact even when a transport caller
// uses json.Unmarshal rather than configuring its decoder with UseNumber.
func (r *Report) UnmarshalJSON(raw []byte) error {
	type plain Report
	var decoded plain
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	if e := d.Decode(&decoded); e != nil {
		return e
	}
	for i := range decoded.Mutations {
		m := &decoded.Mutations[i]
		if n, e := number(m.Value, m.NumberType); e == nil {
			m.Value = n
		}
		if h := m.Delta; h != nil {
			normalizeHistogram(h, m.NumberType)
		}
	}
	*r = Report(decoded)
	return nil
}
func normalizeHistogram(h *Histogram, typ string) {
	if n, e := number(h.Sum, typ); e == nil {
		h.Sum = n
	}
	if n, e := number(h.Min, typ); e == nil {
		h.Min = n
	}
	if n, e := number(h.Max, typ); e == nil {
		h.Max = n
	}
}
func (s *Snapshot) UnmarshalJSON(raw []byte) error {
	type plain Snapshot
	var decoded plain
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if e := d.Decode(&decoded); e != nil {
		return e
	}
	for name, points := range decoded.Series {
		for i := range points {
			p := &points[i]
			if n, e := number(p.Value, p.NumberType); e == nil {
				p.Value = n
			}
			if p.Histogram != nil {
				normalizeHistogram(p.Histogram, p.NumberType)
			}
		}
		decoded.Series[name] = points
	}
	if decoded.Coverage.CPU.ExcludedSeconds == nil {
		decoded.Coverage.CPU.ExcludedSeconds = map[string]float64{}
	}
	*s = Snapshot(decoded)
	return nil
}
