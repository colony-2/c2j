// Package metricshistory reads c2j reports from an observed committed history.
// All derived views use metrics.Reduce; final snapshots are never mutations.
package metricshistory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const SnapshotMetadataKey = "c2j_metrics"
const MaxChapters = 1000000

type pinnedRuntime struct {
	jobdb.JobRunReader
	key      jobdb.JobKey
	chapters []jobdb.Chapter
}

func (p pinnedRuntime) ListChapters(ctx context.Context, q jobdb.ListChaptersRequest) ([]jobdb.Chapter, error) {
	if q.JobKey != p.key {
		return nil, fmt.Errorf("metrics read cannot cross job history")
	}
	var out []jobdb.Chapter
	for _, ch := range p.chapters {
		if ch.Ordinal >= q.StartOrdinal && (q.EndOrdinal == nil || ch.Ordinal <= *q.EndOrdinal) {
			out = append(out, ch)
		}
	}
	return out, nil
}

// Read freezes the frontier with one complete ListChapters call. That API returns
// the whole range (including remote backends), not a cursor page. Pin it while
// JobDB reconstructs compact/restarted history; late writes belong to the next read.
func Read(ctx context.Context, runtime jobdb.JobRunReader, key jobdb.JobKey, q metrics.Query) (metrics.Result, error) {
	if e := q.Validate(); e != nil {
		return metrics.Result{}, e
	}
	chapters, e := runtime.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
	if e != nil {
		return Unavailable("metric history read failed"), e
	}
	if len(chapters) > MaxChapters {
		return Unavailable("metric history scan limit exceeded"), fmt.Errorf("metric history scan limit exceeded")
	}
	sort.Slice(chapters, func(i, j int) bool { return chapters[i].Ordinal < chapters[j].Ordinal })
	frontier := int64(-1)
	for _, ch := range chapters {
		if ch.Ordinal != frontier+1 {
			return Unavailable("metric history contains a gap or duplicate chapter"), fmt.Errorf("incomplete metric history")
		}
		frontier = ch.Ordinal
	}
	if frontier < 0 {
		return Unavailable("metric history is empty"), fmt.Errorf("metric history is empty")
	}
	run, e := jobdb.GetJobRun(ctx, pinnedRuntime{runtime, key, chapters}, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true})
	if e != nil {
		return Unavailable("metric history reconstruction failed"), e
	}
	result, e := metrics.Reduce(Records(run), q, frontier)
	if e == nil && result.Snapshot.Coverage.History != "complete" {
		e = fmt.Errorf("metric history %s", result.Snapshot.Coverage.History)
	}
	return result, e
}
func Unavailable(diagnostic string) metrics.Result {
	r := metrics.Result{Snapshot: metrics.Empty(0)}
	r.Snapshot.Coverage.History = "unavailable"
	r.Snapshot.Coverage.Diagnostics = []string{diagnostic}
	return r
}

// Records uses native task declarations to distinguish material task attempts
// from scope/catch/synthetic/final outputs. Physical result ordinals are retained.
func Records(run jobdb.GetJobRunResponse) []metrics.Record {
	tasks := map[string]bool{}
	for _, entry := range run.Execution {
		if entry.Record.Kind == "task" {
			var descriptor struct {
				Call struct {
					TaskType string `json:"task_type"`
				} `json:"call"`
			}
			if json.Unmarshal(entry.Record.Descriptor, &descriptor) == nil && descriptor.Call.TaskType == task.RecipeStatusTaskType {
				continue
			}
			tasks[entry.Record.ID] = true
		}
	}
	var out []metrics.Record
	for _, entry := range run.Execution {
		r := entry.Record
		if r.Kind != "result" && r.Kind != "attempt" {
			continue
		}
		split := strings.LastIndex(r.ID, "/attempt:")
		if split < 0 {
			continue
		}
		id := r.ID[:split]
		if !tasks[id] {
			continue
		}
		attempt, e := strconv.Atoi(r.ID[split+9:])
		if e != nil || attempt < 1 {
			continue
		}
		channels := metrics.Channels{Runtime: metrics.Channel{Status: "absent"}, Producer: metrics.Channel{Status: "absent"}}
		if r.Kind == "attempt" {
			channels.Runtime.Status = "pending"
			channels.Producer.Status = "pending"
		} else if r.Output != nil {
			channels = Decode(r.Output.Data)
		}
		for _, c := range []struct {
			name string
			data metrics.Channel
		}{{"runtime", channels.Runtime}, {"producer", channels.Producer}} {
			out = append(out, metrics.Record{Tenant: run.Job.JobKey.TenantId, Job: run.Job.JobKey.JobId, Task: id, Attempt: attempt, Ordinal: entry.Ordinal, Channel: c.name, ChannelReport: c.data})
		}
	}
	// Old histories have absent reports. They are never represented by measured zeros.
	if len(run.Execution) == 0 {
		for _, ja := range run.Attempts {
			for _, t := range ja.Tasks {
				for _, a := range t.Attempts {
					channels := metrics.Channels{Runtime: metrics.Channel{Status: "absent"}, Producer: metrics.Channel{Status: "absent"}}
					if a.Output != nil {
						channels = Decode(a.Output.Data)
					}
					for _, c := range []struct {
						name string
						data metrics.Channel
					}{{"runtime", channels.Runtime}, {"producer", channels.Producer}} {
						out = append(out, metrics.Record{Tenant: run.Job.JobKey.TenantId, Job: run.Job.JobKey.JobId, Task: t.TaskRunID, Attempt: a.Attempt, Ordinal: a.Ordinal, Channel: c.name, ChannelReport: c.data})
					}
				}
			}
		}
	}
	return out
}
func Decode(raw []byte) metrics.Channels {
	absent := metrics.Channels{Runtime: metrics.Channel{Status: "absent"}, Producer: metrics.Channel{Status: "absent"}}
	var env task.OutputEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return absent
	}
	if env.Kind != task.OutputKindActivityInvocationOutput && env.Kind != task.OutputKindInternalResult {
		return absent
	}
	if env.Version == 1 {
		return absent
	}
	if env.Version != task.OutputEnvelopeVersion {
		return metrics.Channels{Runtime: metrics.Channel{Status: "unsupported"}, Producer: metrics.Channel{Status: "unsupported"}}
	}
	if env.Metrics == nil {
		return absent
	}
	return *env.Metrics
}
func Stored(ch jobdb.Chapter) (*metrics.Snapshot, error) {
	v, ok := ch.Metadata.Fields["metadata"].Map[SnapshotMetadataKey]
	if !ok {
		return nil, nil
	}
	if v.Kind != jobdb.ChapterMetadataString {
		return nil, fmt.Errorf("invalid stored metric snapshot")
	}
	var snapshot metrics.Snapshot
	d := json.NewDecoder(strings.NewReader(v.String))
	d.UseNumber()
	if d.Decode(&snapshot) != nil || snapshot.Version != metrics.Version || snapshot.ReducerVersion != metrics.ReducerVersion {
		return nil, fmt.Errorf("unsupported stored metric snapshot")
	}
	return &snapshot, nil
}
