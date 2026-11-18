package executionruntime

import (
	"context"
	"encoding/json"

	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/metricshistory"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func (l *lease) finalizeMetrics(ctx context.Context, req jobdb.CompleteExecutionRequest) jobdb.CompleteExecutionRequest {
	if req.Chapter == nil || l.Job().JobKey.JobId == "" || l.Route().JobType != starter.RecipeJobType {
		return req
	}
	// Copy caller-owned metadata. Existing final chapters already contain the
	// winning snapshot after uncertain completion recovery; preserve its exact bytes.
	chapter := *req.Chapter
	chapter.Metadata.Fields = map[string]jobdb.ChapterMetadataValue{}
	for k, v := range req.Chapter.Metadata.Fields {
		chapter.Metadata.Fields[k] = v
	}
	meta := chapter.Metadata.Fields["metadata"]
	fields := map[string]jobdb.ChapterMetadataValue{}
	for k, v := range meta.Map {
		fields[k] = v
	}
	meta.Kind = jobdb.ChapterMetadataMap
	meta.Map = fields
	if _, ok := fields[metricshistory.SnapshotMetadataKey]; ok {
		return req
	}
	l.runtime.mu.Lock()
	cached := l.metricsSnapshot
	l.runtime.mu.Unlock()
	if cached == nil {
		cutoff := chapter.Ordinal - 1
		result, err := metricshistory.Read(ctx, l.runtime.WorkflowRuntime, l.Job().JobKey, metrics.Query{ThroughOrdinal: &cutoff})
		if err != nil {
			result = metricshistory.Unavailable("final metric snapshot unavailable; inspect history to retry")
		}
		result.Snapshot.Source = "final_chapter"
		raw, err := json.Marshal(result.Snapshot)
		if err != nil {
			fallback := metricshistory.Unavailable("final metric snapshot encoding failed")
			raw, _ = json.Marshal(fallback.Snapshot)
		}
		value := jobdb.ChapterMetadataValue{Kind: jobdb.ChapterMetadataString, String: string(raw)}
		l.runtime.mu.Lock()
		if l.metricsSnapshot == nil {
			l.metricsSnapshot = &value
		}
		cached = l.metricsSnapshot
		l.runtime.mu.Unlock()
	}
	fields[metricshistory.SnapshotMetadataKey] = *cached
	chapter.Metadata.Fields["metadata"] = meta
	req.Chapter = &chapter
	return req
}
