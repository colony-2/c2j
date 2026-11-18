package metrics

import (
	"context"
	"encoding/json"
	"sync"
)

// Collector is bounded for the entire attempt, including concurrent native emissions.
// Invalidating it discards the whole channel and releases all retained mutations.
type Collector struct {
	mu         sync.Mutex
	runtime    bool
	report     Report
	status     string
	diagnostic string
	check      func(any) error
}

func NewCollector(check func(any) error) *Collector {
	return &Collector{report: Report{Version: Version, Mutations: []Mutation{}}, status: "absent", check: check}
}

// NewRuntimeCollector is used only by c2j instrumentation. Producer code receives
// only FromContext's independent collector, never this handle.
func NewRuntimeCollector(check func(any) error) *Collector {
	c := NewCollector(check)
	c.runtime = true
	c.status = "valid"
	return c
}
func (c *Collector) Invalidate(status, diagnostic string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidate(status, diagnostic)
}
func (c *Collector) invalidate(status, diagnostic string) {
	switch status {
	case "invalid", "unsupported", "unavailable":
	default:
		status = "invalid"
	}
	c.status = status
	c.diagnostic = safeDiagnostic(diagnostic)
	c.report.Mutations = nil
}
func (c *Collector) Emit(m Mutation) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status != "valid" && c.status != "absent" {
		return
	}
	if c.check != nil && c.check(m) != nil {
		c.invalidate("invalid", "metric report contains secret data")
		return
	}
	r, e := Validate(&Report{Version: Version, Mutations: []Mutation{m}}, c.runtime)
	if e != nil {
		c.invalidate("invalid", e.Error())
		return
	}
	m = r.Mutations[0]
	// Coalesce commutative deltas and repeated observations; gauges keep only the
	// last contiguous set. Separate gauge mutations preserve cross-channel order.
	found := -1
	for i, p := range c.report.Mutations {
		if descriptorKey(p.Descriptor) == descriptorKey(m.Descriptor) && seriesKey(p) == seriesKey(m) {
			found = i
			break
		}
	}
	if found >= 0 {
		prior := c.report.Mutations[found]
		switch m.Kind {
		case "counter":
			m.Value, e = add(prior.Value, m.Value)
		case "gauge":
		case "histogram":
			m.Delta, e = merge(delta(prior), delta(m))
			m.Op = "merge"
			m.Value = nil
		}
	}
	if e != nil {
		c.invalidate("invalid", e.Error())
		return
	}
	trial := append([]Mutation(nil), c.report.Mutations...)
	if found < 0 {
		trial = append(trial, m)
	} else {
		trial[found] = m
	}
	b, e := json.Marshal(Report{Version: Version, Mutations: trial})
	if e != nil || len(b) > MaxBytes || len(trial) > MaxMutations {
		c.invalidate("invalid", "metric collector limit exceeded")
		return
	}
	c.report.Mutations = trial
	c.status = "valid"
}

// Import preserves extension mutation operations and their order.
func (c *Collector) Import(raw []byte) {
	if c == nil {
		return
	}
	r, diag := DecodeReport(raw)
	if diag != "" {
		status := "invalid"
		if diag == "unsupported metric report version" {
			status = "unsupported"
		}
		c.Invalidate(status, diag)
		return
	}
	if c.check != nil && c.check(r) != nil {
		c.Invalidate("invalid", "metric report contains secret data")
		return
	}
	normalized, e := Validate(r, c.runtime)
	if e != nil {
		c.Invalidate("invalid", e.Error())
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status != "absent" {
		c.invalidate("invalid", "multiple producer reports")
		return
	}
	c.report = *normalized
	c.status = "valid"
}
func (c *Collector) Snapshot() Channel {
	if c == nil {
		return Channel{Status: "absent"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status == "valid" && c.check != nil && c.check(c.report) != nil {
		c.invalidate("invalid", "metric report contains secret data")
	}
	out := Channel{Status: c.status, Diagnostic: c.diagnostic}
	if c.status == "valid" {
		b, _ := json.Marshal(c.report)
		r, _ := DecodeReport(b)
		out.Report = r
	}
	return out
}

type collectorKey struct{}

func WithCollector(ctx context.Context, c *Collector) context.Context {
	return context.WithValue(ctx, collectorKey{}, c)
}
func FromContext(ctx context.Context) *Collector {
	c, _ := ctx.Value(collectorKey{}).(*Collector)
	return c
}
func (c *Collector) AddInt(name, unit string, labels map[string]string, n int64) {
	c.Emit(Mutation{Descriptor: Descriptor{Name: name, Kind: "counter", NumberType: "int64", Unit: unit}, Labels: labels, Op: "add", Value: n})
}
func (c *Collector) AddFloat(name, unit string, labels map[string]string, n float64) {
	c.Emit(Mutation{Descriptor: Descriptor{Name: name, Kind: "counter", NumberType: "float64", Unit: unit}, Labels: labels, Op: "add", Value: n})
}
func (c *Collector) Observe(name, unit string, labels map[string]string, n float64) {
	c.Emit(Mutation{Descriptor: Descriptor{Name: name, Kind: "histogram", NumberType: "float64", Unit: unit}, Labels: labels, Op: "observe", Value: n})
}

// Diagnostic text is from a fixed vocabulary, never producer values or errors.
func safeDiagnostic(s string) string {
	switch s {
	case "missing metric report", "unsupported metric report version", "invalid metric report encoding", "metric report exceeds byte limit", "metric mutation limit exceeded", "invalid metric name or unit", "reserved metric name", "invalid metric numeric type", "metric label limit exceeded", "invalid metric label", "metric boundary limit exceeded", "invalid histogram boundaries", "bounds require histogram", "invalid metric operation", "negative counter delta", "observe cannot include delta", "merge requires delta", "invalid histogram operation", "invalid metric kind", "invalid histogram counts", "metric count overflow", "histogram bucket count mismatch", "invalid empty histogram", "inconsistent histogram extrema", "histogram extrema outside buckets", "histogram buckets outside extrema", "histogram sum outside extrema", "invalid singleton histogram", "missing metric number", "metric requires exact int64", "invalid metric number", "nonfinite metric number", "metric integer overflow", "metric float overflow", "invalid metric report shape", "metric report contains secret data", "metric collector limit exceeded", "multiple producer reports", "extension metric report unavailable", "metric descriptor conflict", "metric series limit exceeded":
		return s
	default:
		return "invalid metric report"
	}
}
