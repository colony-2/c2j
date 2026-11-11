package input

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// GetJobHistorySnapshotRequest selects an existing snapshot, or captures one when
// SnapshotToken is empty. It works even when no input occurrences were recorded.
type GetJobHistorySnapshotRequest struct{ TenantID, JobID, SnapshotToken string }

type HistoryMergeEntry struct {
	EntryID                string          `json:"entry_id"`
	TenantID               string          `json:"tenant_id"`
	JobID                  string          `json:"job_id"`
	JobAttempt             int             `json:"job_attempt"`
	InvocationID           string          `json:"invocation_id"`
	TaskAttempt            int             `json:"task_attempt"`
	ResultOrdinal          int64           `json:"result_ordinal"`
	CompletedAt            *time.Time      `json:"completed_at,omitempty"`
	Operation              string          `json:"operation"`
	EvidenceAvailability   string          `json:"evidence_availability"`
	CandidateSHA           string          `json:"candidate_sha,omitempty"`
	CandidateSubject       *string         `json:"candidate_subject,omitempty"`
	PublicationBranch      string          `json:"publication_branch,omitempty"`
	PublicationDisposition string          `json:"publication_disposition,omitempty"`
	Output                 json.RawMessage `json:"output,omitempty"`
}

// HistoryEvent is an ordering coordinate, not an additional input occurrence.
// Equal ordinals do not establish causality.
type HistoryEvent struct {
	Ordinal      int64  `json:"ordinal"`
	InvocationID string `json:"invocation_id"`
	Kind         string `json:"kind"`
	EntryID      string `json:"entry_id"`
}

type JobHistorySnapshot struct {
	TenantID         string              `json:"tenant_id"`
	JobID            string              `json:"job_id"`
	SnapshotToken    string              `json:"snapshot_token"`
	SnapshotRevision int64               `json:"snapshot_revision"`
	ObservedAt       time.Time           `json:"observed_at"`
	Merges           []HistoryMergeEntry `json:"merges"`
	Events           []HistoryEvent      `json:"events"`
}

// HistorySnapshotService is an additive capability implemented by NewHistoryService.
type HistorySnapshotService interface {
	GetJobHistorySnapshot(context.Context, GetJobHistorySnapshotRequest) (*JobHistorySnapshot, error)
}

// GetJobHistorySnapshot leaves HistoryService implementations source compatible.
// Services without the sibling capability return ErrHistoryUnsupported.
func GetJobHistorySnapshot(ctx context.Context, service HistoryService, req GetJobHistorySnapshotRequest) (*JobHistorySnapshot, error) {
	s, ok := service.(HistorySnapshotService)
	if !ok {
		return nil, &HistoryError{Kind: ErrHistoryUnsupported, Capability: "shared_snapshot"}
	}
	return s.GetJobHistorySnapshot(ctx, req)
}

func (s *historyService) GetJobHistorySnapshot(ctx context.Context, req GetJobHistorySnapshotRequest) (*JobHistorySnapshot, error) {
	key, err := historyKey(req.TenantID, req.JobID)
	if err != nil {
		return nil, err
	}
	snap, token, err := s.snapshot(ctx, key, req.SnapshotToken)
	if err != nil {
		return nil, err
	}
	return &JobHistorySnapshot{TenantID: key.TenantId, JobID: key.JobId, SnapshotToken: token, SnapshotRevision: snap.highWater, ObservedAt: snap.created, Merges: historyCopy(snap.merges), Events: historyCopy(snap.events)}, nil
}

func historyMergeProjection(key jobdb.JobKey, run jobdb.GetJobRunResponse) []HistoryMergeEntry {
	out := []HistoryMergeEntry{}
	tasks := map[string]historyTaskDescriptor{}
	for _, e := range run.Execution {
		r := e.Record
		if r.Kind == "task" {
			var d historyTaskDescriptor
			if json.Unmarshal(r.Descriptor, &d) == nil {
				tasks[r.ID] = d
			}
			continue
		}
		if r.Kind != "result" || r.ErrorKind != "" {
			continue
		}
		typ := tasks[historyTaskID(r.ID)].Call.TaskType
		// Only the actual publication step is selected, never containers or titles.
		if typ != "merge:merge" && typ != "squashrebasemerge:merge" {
			continue
		}
		m := HistoryMergeEntry{EntryID: historyID("hme1_", key.TenantId, key.JobId, r.ID, strconv.FormatInt(e.Ordinal, 10)), TenantID: key.TenantId, JobID: key.JobId, JobAttempt: historyJobAttempt(r.ID), InvocationID: r.ID, TaskAttempt: historyAttempt(r.ID), ResultOrdinal: e.Ordinal, Operation: typ, EvidenceAvailability: "unavailable"}
		if !r.At.IsZero() {
			at := r.At
			m.CompletedAt = &at
		}
		raw, _, err := historyOutput(r.Output)
		if err == nil {
			m.Output = raw
			var fields struct {
				SHA         string  `json:"candidate_sha"`
				Subject     *string `json:"candidate_subject"`
				Branch      string  `json:"publication_branch"`
				Disposition string  `json:"publication_disposition"`
			}
			if json.Unmarshal(raw, &fields) == nil {
				m.CandidateSHA = fields.SHA
				m.CandidateSubject = fields.Subject
				m.PublicationBranch = fields.Branch
				m.PublicationDisposition = fields.Disposition
				if fields.SHA != "" && fields.Subject != nil && fields.Branch != "" && (fields.Disposition == "published" || fields.Disposition == "already_present" || fields.Disposition == "no_changes") {
					m.EvidenceAvailability = "recorded"
				}
			}
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.ResultOrdinal != b.ResultOrdinal {
			return a.ResultOrdinal < b.ResultOrdinal
		}
		if a.InvocationID != b.InvocationID {
			return a.InvocationID < b.InvocationID
		}
		return a.EntryID < b.EntryID
	})
	return out
}

func historyEvents(entries []JobInputStoryEntryDetail, merges []HistoryMergeEntry, run jobdb.GetJobRunResponse) []HistoryEvent {
	out := []HistoryEvent{}
	for _, e := range entries {
		if e.Locator.GenerationOrdinal != nil {
			out = append(out, HistoryEvent{*e.Locator.GenerationOrdinal, e.Locator.InvocationID, "request_generation", e.EntryID})
		}
		if e.Locator.AcceptedOrdinal != nil {
			invocation := e.Locator.InvocationID
			for _, record := range run.Execution {
				if record.Ordinal != *e.Locator.AcceptedOrdinal || record.Record.Kind != "result" {
					continue
				}
				for _, id := range e.Locator.CollectionInvocationIDs {
					if record.Record.ID == id || strings.HasPrefix(record.Record.ID, id+"/attempt:") {
						invocation = record.Record.ID
					}
				}
			}
			out = append(out, HistoryEvent{*e.Locator.AcceptedOrdinal, invocation, "response_acceptance", e.EntryID})
		}
	}
	for _, m := range merges {
		out = append(out, HistoryEvent{m.ResultOrdinal, m.InvocationID, "merge_completion", m.EntryID})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Ordinal != b.Ordinal {
			return a.Ordinal < b.Ordinal
		}
		if a.InvocationID != b.InvocationID {
			return a.InvocationID < b.InvocationID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.EntryID < b.EntryID
	})
	return out
}
