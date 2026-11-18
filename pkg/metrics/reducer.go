package metrics

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Segments parses native occurrence IDs without splitting slashes inside quoted sites.
func Segments(id string) ([]string, error) {
	if id == "" || id[0] != '/' {
		return nil, fmt.Errorf("selector must be a native occurrence ID")
	}
	var out []string
	start, quoted, escaped := 1, false, false
	for i := 1; i <= len(id); i++ {
		if i < len(id) {
			c := id[i]
			if escaped {
				escaped = false
				continue
			}
			if quoted && c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				quoted = !quoted
			}
			if c != '/' || quoted {
				continue
			}
		}
		s := id[start:i]
		if s == "" || quoted {
			return nil, fmt.Errorf("invalid occurrence selector")
		}
		switch {
		case strings.HasPrefix(s, "attempt:"):
			n, e := strconv.Atoi(strings.TrimPrefix(s, "attempt:"))
			if e != nil || n < 1 {
				return nil, fmt.Errorf("invalid occurrence attempt")
			}
		case strings.HasPrefix(s, "scope:"), strings.HasPrefix(s, "task:"):
			p := strings.IndexByte(s, ':')
			j := strings.LastIndex(s, "#")
			if j <= p+1 {
				return nil, fmt.Errorf("invalid occurrence selector")
			}
			if _, e := strconv.Unquote(s[p+1 : j]); e != nil {
				return nil, fmt.Errorf("invalid occurrence site")
			}
			n, e := strconv.Atoi(s[j+1:])
			if e != nil || n < 1 {
				return nil, fmt.Errorf("invalid occurrence counter")
			}
		default:
			return nil, fmt.Errorf("invalid occurrence segment")
		}
		out = append(out, s)
		start = i + 1
	}
	return out, nil
}
func (q Query) Validate() error {
	if q.Attempt < 0 {
		return fmt.Errorf("attempt must be positive")
	}
	if q.Attempt > 0 && q.Task == "" && q.Scope == "" {
		return fmt.Errorf("--attempt requires --task or --scope")
	}
	if q.ThroughOrdinal != nil && *q.ThroughOrdinal < 0 {
		return fmt.Errorf("through-ordinal must be nonnegative")
	}
	for _, s := range []struct{ id, kind string }{{q.Task, "task:"}, {q.Scope, "scope:"}} {
		if s.id == "" {
			continue
		}
		parts, e := Segments(s.id)
		if e != nil {
			return e
		}
		last := parts[len(parts)-1]
		if !strings.HasPrefix(last, s.kind) {
			return fmt.Errorf("selector must end at a %s occurrence", strings.TrimSuffix(s.kind, ":"))
		}
	}
	return nil
}
func (q Query) selects(r Record) bool {
	if q.Task != "" && q.Task != r.Task {
		return false
	}
	if q.Scope != "" {
		a, e := Segments(q.Scope)
		if e != nil {
			return false
		}
		b, e := Segments(r.Task)
		if e != nil || len(b) <= len(a) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		if q.Task == "" && q.Attempt > 0 && b[len(a)] != "attempt:"+strconv.Itoa(q.Attempt) {
			return false
		}
	}
	return q.Task == "" || q.Attempt == 0 || q.Attempt == r.Attempt
}

type state struct {
	descriptors map[string]Descriptor
	points      map[string]Point
}

func newState() *state { return &state{map[string]Descriptor{}, map[string]Point{}} }

// stage validates all mutations against the canonical state before admitting any.
func (s *state) apply(r Record, report *Report, name string) error {
	descriptors := map[string]Descriptor{}
	points := map[string]Point{}
	for index, m := range report.Mutations {
		d, exists := descriptors[m.Name]
		if !exists {
			d, exists = s.descriptors[m.Name]
		}
		if exists && descriptorKey(d) != descriptorKey(m.Descriptor) {
			return fmt.Errorf("metric descriptor conflict")
		}
		descriptors[m.Name] = m.Descriptor
		if name != "" && m.Name != name {
			continue
		}
		key := seriesKey(m)
		p, ok := points[key]
		if !ok {
			p, ok = s.points[key]
		}
		if !ok {
			p = Point{Descriptor: m.Descriptor, Labels: m.Labels}
			if m.Kind != "histogram" {
				p.Value = zero(m.NumberType)
			}
		}
		var e error
		switch m.Kind {
		case "counter":
			p.Value, e = add(p.Value, m.Value)
		case "gauge":
			p.Value = m.Value
			p.Position = &Position{Task: r.Task, Attempt: r.Attempt, Ordinal: r.Ordinal, Channel: r.Channel, Mutation: index}
		case "histogram":
			p.Histogram, e = merge(p.Histogram, delta(m))
		}
		if e != nil {
			return e
		}
		points[key] = p
	}
	added := 0
	for k := range points {
		if _, ok := s.points[k]; !ok {
			added++
		}
	}
	if len(s.points)+added > MaxSeries {
		return fmt.Errorf("metric series limit exceeded")
	}
	for k, d := range descriptors {
		s.descriptors[k] = d
	}
	for k, p := range points {
		s.points[k] = p
	}
	return nil
}

// Reduce admits the complete committed prefix before selecting scope/task/name.
// It never subtracts snapshots, mutates input, or treats missing measurements as zero.
func Reduce(records []Record, q Query, frontier int64) (Result, error) {
	if e := q.Validate(); e != nil {
		return Result{}, e
	}
	cutoff := frontier
	if q.ThroughOrdinal != nil && *q.ThroughOrdinal < cutoff {
		cutoff = *q.ThroughOrdinal
	}
	out := Result{Snapshot: Empty(cutoff)}
	selected := newState()
	ordered := append([]Record(nil), records...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Ordinal != b.Ordinal {
			return a.Ordinal < b.Ordinal
		}
		if a.Channel != b.Channel {
			return a.Channel == "runtime"
		}
		return false
	})
	completed := map[string]bool{}
	identityOf := func(r Record) string {
		b, _ := json.Marshal([]any{r.Tenant, r.Job, r.Task, r.Attempt, r.Channel})
		return string(b)
	}
	for _, r := range ordered {
		if r.Ordinal <= cutoff && r.ChannelReport.Status != "pending" {
			completed[identityOf(r)] = true
		}
	}
	admission := newAdmission()
	for _, record := range ordered {
		if record.Ordinal > cutoff {
			continue
		}
		key := identityOf(record)
		if record.ChannelReport.Status == "pending" && completed[key] {
			continue
		}
		c, fresh, inconsistent := admission.consume(record)
		if inconsistent {
			out.Snapshot.Coverage.History = "partial"
			out.Snapshot.Coverage.Diagnostics = append(out.Snapshot.Coverage.Diagnostics, "inconsistent duplicate metric report")
		}
		if !fresh {
			continue
		}
		if !q.selects(record) {
			continue
		}
		if c.Status == "valid" {
			if e := selected.apply(record, c.Report, q.Name); e != nil {
				out.Snapshot.Coverage.History = "partial"
				out.Snapshot.Coverage.Diagnostics = append(out.Snapshot.Coverage.Diagnostics, e.Error())
				c = Channel{Status: "invalid", Diagnostic: e.Error()}
			}
		}
		addCoverage(&out.Snapshot.Coverage, record.Channel, c)
		if q.Deltas {
			if q.Name != "" && c.Report != nil {
				r := *c.Report
				r.Mutations = nil
				for _, m := range c.Report.Mutations {
					if m.Name == q.Name {
						r.Mutations = append(r.Mutations, m)
					}
				}
				c.Report = &r
			}
			record.ChannelReport = c
			out.Reports = append(out.Reports, record)
		}
	}
	keys := make([]string, 0, len(selected.points))
	for k := range selected.points {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := selected.points[k]
		out.Snapshot.Series[p.Name] = append(out.Snapshot.Series[p.Name], p)
	}
	return out, nil
}
func countStatus(counts map[string]map[string]int, group, status string) {
	if counts[group] == nil {
		counts[group] = map[string]int{}
	}
	counts[group][status]++
}
