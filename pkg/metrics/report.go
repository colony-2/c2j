package metrics

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"unicode/utf8"
)

func textOK(s string, max int) bool     { return len(s) <= max && utf8.ValidString(s) }
func descriptorKey(d Descriptor) string { b, _ := json.Marshal(d); return string(b) }
func seriesKey(m Mutation) string {
	labels := m.Labels
	if len(labels) == 0 {
		labels = map[string]string{}
	}
	raw, _ := json.Marshal([]any{m.Name, labels})
	return string(raw)
}

// Validate returns an owned normalized report. No part of a rejected report is usable.
func Validate(r *Report, runtime bool) (*Report, error) {
	if r == nil {
		return nil, fmt.Errorf("missing metric report")
	}
	if r.Version != Version {
		return nil, fmt.Errorf("unsupported metric report version")
	}
	b, e := json.Marshal(r)
	if e != nil {
		return nil, fmt.Errorf("invalid metric report encoding")
	}
	if len(b) > MaxBytes {
		return nil, fmt.Errorf("metric report exceeds byte limit")
	}
	if len(r.Mutations) > MaxMutations {
		return nil, fmt.Errorf("metric mutation limit exceeded")
	}
	// Round-trip also takes ownership of producer maps, numbers and slices.
	out, diag := DecodeReport(b)
	if diag != "" {
		return nil, fmt.Errorf("%s", diag)
	}
	for i := range out.Mutations {
		m := &out.Mutations[i]
		if m.Name == "" || !textOK(m.Name, MaxNameBytes) || !textOK(m.Unit, MaxNameBytes) {
			return nil, fmt.Errorf("invalid metric name or unit")
		}
		if strings.HasPrefix(m.Name, "c2j_") && !runtime {
			return nil, fmt.Errorf("reserved metric name")
		}
		if m.NumberType != "int64" && m.NumberType != "float64" {
			return nil, fmt.Errorf("invalid metric numeric type")
		}
		if len(m.Labels) > MaxLabels {
			return nil, fmt.Errorf("metric label limit exceeded")
		}
		for k, v := range m.Labels {
			if k == "" || !textOK(k, MaxNameBytes) || !textOK(v, MaxLabelBytes) {
				return nil, fmt.Errorf("invalid metric label")
			}
		}
		if len(m.Bounds) > MaxBounds {
			return nil, fmt.Errorf("metric boundary limit exceeded")
		}
		for j, v := range m.Bounds {
			if v == 0 {
				m.Bounds[j] = 0
			}
			if math.IsNaN(v) || math.IsInf(v, 0) || (j > 0 && v <= m.Bounds[j-1]) {
				return nil, fmt.Errorf("invalid histogram boundaries")
			}
		}
		if len(m.Bounds) == 0 {
			m.Bounds = nil
		}
		if m.Kind != "histogram" && len(m.Bounds) > 0 {
			return nil, fmt.Errorf("bounds require histogram")
		}
		switch m.Kind {
		case "counter", "gauge":
			expected := "add"
			if m.Kind == "gauge" {
				expected = "set"
			}
			if m.Op != expected || m.Delta != nil {
				return nil, fmt.Errorf("invalid metric operation")
			}
			m.Value, e = number(m.Value, m.NumberType)
			if e != nil {
				return nil, e
			}
			if m.Kind == "counter" && negative(m.Value) {
				return nil, fmt.Errorf("negative counter delta")
			}
		case "histogram":
			switch m.Op {
			case "observe":
				if m.Delta != nil {
					return nil, fmt.Errorf("observe cannot include delta")
				}
				m.Value, e = number(m.Value, m.NumberType)
				if e != nil {
					return nil, e
				}
			case "merge":
				if m.Value != nil || m.Delta == nil {
					return nil, fmt.Errorf("merge requires delta")
				}
				if e = validateHistogram(m.Delta, m.NumberType, m.Bounds); e != nil {
					return nil, e
				}
			default:
				return nil, fmt.Errorf("invalid histogram operation")
			}
		default:
			return nil, fmt.Errorf("invalid metric kind")
		}
	}
	return out, nil
}
func validateHistogram(h *Histogram, typ string, bounds []float64) error {
	var e error
	if h.Count < 0 || len(h.BucketCounts) != len(bounds)+1 {
		return fmt.Errorf("invalid histogram counts")
	}
	var count int64
	for _, n := range h.BucketCounts {
		count, e = addCount(count, n)
		if e != nil {
			return e
		}
	}
	if count != h.Count {
		return fmt.Errorf("histogram bucket count mismatch")
	}
	h.Sum, e = number(h.Sum, typ)
	if e != nil {
		return e
	}
	if count == 0 {
		if h.Min != nil || h.Max != nil || compare(h.Sum, zero(typ)) != 0 {
			return fmt.Errorf("invalid empty histogram")
		}
		return nil
	}
	h.Min, e = number(h.Min, typ)
	if e != nil {
		return e
	}
	h.Max, e = number(h.Max, typ)
	if e != nil {
		return e
	}
	if compare(h.Min, h.Max) > 0 {
		return fmt.Errorf("inconsistent histogram extrema")
	}
	lo, hi := bucket(h.Min, bounds), bucket(h.Max, bounds)
	if h.BucketCounts[lo] == 0 || h.BucketCounts[hi] == 0 {
		return fmt.Errorf("histogram extrema outside buckets")
	}
	for j, n := range h.BucketCounts {
		if (j < lo || j > hi) && n != 0 {
			return fmt.Errorf("histogram buckets outside extrema")
		}
	}
	// Check the mean using division to avoid overflow in extrema*count.
	if typ == "int64" {
		sum := big.NewInt(h.Sum.(int64))
		n := big.NewInt(count)
		lo := new(big.Int).Mul(big.NewInt(h.Min.(int64)), n)
		hi := new(big.Int).Mul(big.NewInt(h.Max.(int64)), n)
		if sum.Cmp(lo) < 0 || sum.Cmp(hi) > 0 {
			return fmt.Errorf("histogram sum outside extrema")
		}
	} else {
		mean := Float(h.Sum) / float64(count)
		tolerance := 1e-12 * math.Max(math.Abs(Float(h.Min)), math.Abs(Float(h.Max)))
		if mean < Float(h.Min)-tolerance || mean > Float(h.Max)+tolerance {
			return fmt.Errorf("histogram sum outside extrema")
		}
	}
	if count == 1 && (compare(h.Min, h.Max) != 0 || compare(h.Sum, h.Min) != 0) {
		return fmt.Errorf("invalid singleton histogram")
	}
	return nil
}
func bucket(v any, bounds []float64) int {
	return sort.Search(len(bounds), func(i int) bool {
		b := bounds[i]
		if n, ok := v.(int64); ok {
			if b >= math.MaxInt64 {
				return true
			}
			if b < math.MinInt64 {
				return false
			}
			return n <= int64(math.Floor(b))
		}
		return v.(float64) <= b
	})
}
func delta(m Mutation) *Histogram {
	if m.Op == "merge" {
		h := *m.Delta
		h.BucketCounts = append([]int64(nil), h.BucketCounts...)
		return &h
	}
	h := &Histogram{Count: 1, Sum: m.Value, Min: m.Value, Max: m.Value, BucketCounts: make([]int64, len(m.Bounds)+1)}
	h.BucketCounts[bucket(m.Value, m.Bounds)] = 1
	return h
}
func merge(a, b *Histogram) (*Histogram, error) {
	if a == nil {
		h := *b
		h.BucketCounts = append([]int64(nil), b.BucketCounts...)
		return &h, nil
	}
	h := *a
	var e error
	h.Count, e = addCount(a.Count, b.Count)
	if e != nil {
		return nil, e
	}
	h.Sum, e = add(a.Sum, b.Sum)
	if e != nil {
		return nil, e
	}
	h.BucketCounts = append([]int64(nil), a.BucketCounts...)
	for i, n := range b.BucketCounts {
		h.BucketCounts[i], e = addCount(h.BucketCounts[i], n)
		if e != nil {
			return nil, e
		}
	}
	if b.Count > 0 {
		if a.Count == 0 || compare(b.Min, a.Min) < 0 {
			h.Min = b.Min
		}
		if a.Count == 0 || compare(b.Max, a.Max) > 0 {
			h.Max = b.Max
		}
	}
	return &h, nil
}
