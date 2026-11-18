package metrics

import (
	"crypto/sha256"
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"sync"
)

// admission is shared by history reduction and incremental execution. A query
// or lexical scope cannot revive a report rejected by the job's canonical state.
type admission struct {
	canonical *state
	seen      map[string][32]byte
}

func newAdmission() *admission {
	return &admission{canonical: newState(), seen: map[string][32]byte{}}
}

func (a *admission) consume(r Record) (Channel, bool, bool) {
	key, _ := json.Marshal([]any{r.Tenant, r.Job, r.Task, r.Attempt, r.Channel})
	raw, _ := json.Marshal(r.ChannelReport)
	hash := sha256.Sum256(raw)
	if prior, ok := a.seen[string(key)]; ok {
		return Channel{}, false, prior != hash
	}
	a.seen[string(key)] = hash
	c := r.ChannelReport
	if c.Status == "valid" {
		normalized, err := Validate(c.Report, r.Channel == "runtime")
		if err == nil {
			err = a.canonical.apply(r, normalized, "")
		}
		if err != nil {
			c = Channel{Status: "invalid", Diagnostic: err.Error(), Phases: c.Phases, CPU: c.CPU}
		} else {
			c.Report = normalized
		}
	} else {
		c.Report = nil
		switch c.Status {
		case "absent", "invalid", "unsupported", "pending", "unavailable":
		default:
			c.Status = "invalid"
		}
		if c.Diagnostic != "" {
			c.Diagnostic = safeDiagnostic(c.Diagnostic)
		}
	}
	return c, true, false
}

func addCoverage(coverage *Coverage, channel string, c Channel) {
	countStatus(coverage.Reports, channel, c.Status)
	for phase, status := range c.Phases {
		countStatus(coverage.Phases, phase, status)
	}
	if c.CPU != nil && c.Status == "valid" && channel == "runtime" {
		cpu := &coverage.CPU
		if c.CPU.Availability == "available" {
			cpu.Availability = "available"
		}
		cpu.MeasuredSeconds += c.CPU.MeasuredSeconds
		cpu.ProcessSeconds += c.CPU.ProcessSeconds
		for reason, n := range c.CPU.ExcludedSeconds {
			cpu.ExcludedSeconds[reason] += n
		}
	}
}

// Accumulator reconstructs one execution invocation as committed results are
// consumed. It retains admission fingerprints and aggregates, never report history.
// A fresh invocation must allocate a fresh accumulator, including on replay.
type Accumulator struct {
	mu        sync.Mutex
	admission *admission
	job       *View
	active    map[*View]bool
	cutoff    int64
}

// View contains a job or lexical occurrence's aggregate. Snapshot returns an
// independent value; callers cannot mutate the accumulator through a projection.
type View struct {
	owner    *Accumulator
	state    *state
	coverage Coverage
}

func NewAccumulator() *Accumulator {
	a := &Accumulator{admission: newAdmission(), active: map[*View]bool{}}
	a.job = a.newView()
	return a
}

func (a *Accumulator) newView() *View {
	return &View{owner: a, state: newState(), coverage: Empty(0).Coverage}
}

func (a *Accumulator) Job() *View { return a.job }

// EnterScope starts a lexical occurrence before its native scope executes.
// Keep it active across that scope's retries and catches, and close it when the
// scope unwinds. Nested scopes receive the same reports without re-admission.
func (a *Accumulator) EnterScope() (*View, func()) {
	a.mu.Lock()
	v := a.newView()
	a.active[v] = true
	a.mu.Unlock()
	return v, func() {
		a.mu.Lock()
		delete(a.active, v)
		a.mu.Unlock()
	}
}

// Consume accepts one committed channel in result order (runtime, then producer).
// Scope/catch replacement outputs must never be passed here.
func (a *Accumulator) Consume(r Record) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Ordinal > a.cutoff {
		a.cutoff = r.Ordinal
	}
	c, fresh, inconsistent := a.admission.consume(r)
	apply := func(v *View) {
		if inconsistent {
			v.coverage.History = "partial"
			v.coverage.Diagnostics = append(v.coverage.Diagnostics, "inconsistent duplicate metric report")
		}
		if !fresh {
			return
		}
		selected := c
		if c.Status == "valid" {
			if err := v.state.apply(r, c.Report, ""); err != nil {
				v.coverage.History = "partial"
				v.coverage.Diagnostics = append(v.coverage.Diagnostics, err.Error())
				selected = Channel{Status: "invalid", Diagnostic: err.Error()}
			}
		}
		addCoverage(&v.coverage, r.Channel, selected)
	}
	apply(a.job)
	for v := range a.active {
		apply(v)
	}
}

func (v *View) Snapshot() Snapshot {
	if v == nil {
		return Empty(0)
	}
	v.owner.mu.Lock()
	defer v.owner.mu.Unlock()
	s := Empty(v.owner.cutoff)
	s.Coverage = v.coverage
	s.Coverage.Reports = cloneCounts(v.coverage.Reports)
	s.Coverage.Phases = cloneCounts(v.coverage.Phases)
	s.Coverage.Diagnostics = slices.Clone(v.coverage.Diagnostics)
	s.Coverage.CPU.ExcludedSeconds = maps.Clone(v.coverage.CPU.ExcludedSeconds)
	keys := make([]string, 0, len(v.state.points))
	for key := range v.state.points {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p := v.state.points[key]
		p.Bounds = slices.Clone(p.Bounds)
		p.Labels = maps.Clone(p.Labels)
		if p.Histogram != nil {
			h := *p.Histogram
			h.BucketCounts = slices.Clone(h.BucketCounts)
			p.Histogram = &h
		}
		if p.Position != nil {
			pos := *p.Position
			p.Position = &pos
		}
		s.Series[p.Name] = append(s.Series[p.Name], p)
	}
	return s
}

func cloneCounts(in map[string]map[string]int) map[string]map[string]int {
	out := make(map[string]map[string]int, len(in))
	for k, v := range in {
		out[k] = maps.Clone(v)
	}
	return out
}
