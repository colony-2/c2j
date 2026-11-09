package input

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// HistoryService reads retained input occurrences. It never submits responses or
// reconstructs missing data by running recipe code. Tokens belong to one service
// instance; applications should retain that instance across requests.
type HistoryService interface {
	ListJobInputStory(context.Context, ListJobInputStoryRequest) (*JobInputStoryPage, error)
	GetJobInputStoryEntry(context.Context, GetJobInputStoryEntryRequest) (*JobInputStoryEntryDetail, error)
	OpenJobInputStoryArtifact(context.Context, OpenJobInputStoryArtifactRequest) (*HistoryArtifact, error)
}

type ListJobInputStoryRequest struct {
	TenantID, JobID string
	PageSize        int
	PageToken       string
}
type GetJobInputStoryEntryRequest struct{ TenantID, JobID, EntryID, SnapshotToken string }
type OpenJobInputStoryArtifactRequest struct{ TenantID, JobID, EntryID, ArtifactID, SnapshotToken string }

var (
	ErrHistoryEntryNotFound    = errors.New("input history entry not found")
	ErrHistoryArtifactNotFound = errors.New("artifact is not a member of the input occurrence")
	ErrHistoryInvalidCursor    = errors.New("invalid input history cursor")
	ErrHistorySnapshotExpired  = errors.New("input history snapshot expired; restart pagination")
	ErrHistoryUnsupported      = errors.New("unsupported input history capability")
)

// HistoryError supports errors.Is for Kind and errors.As for coordinates. Storage
// and authorization errors from the durable reader are propagated unchanged.
type HistoryError struct {
	Kind                            error
	EntryID, ArtifactID, Capability string
	Cause                           error
}

func (e *HistoryError) Error() string {
	if e.Cause != nil {
		return e.Kind.Error() + ": " + e.Cause.Error()
	}
	return e.Kind.Error()
}
func (e *HistoryError) Is(target error) bool { return target == e.Kind }
func (e *HistoryError) Unwrap() error        { return e.Cause }

type HistoryDiagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	EntryID string `json:"entry_id,omitempty"`
	Ordinal int64  `json:"ordinal,omitempty"`
}

// HistoryValue keeps absence separate from JSON null, including exact numbers.
// A nil Raw value with Present=false means no field was recorded.
type HistoryValue struct {
	Present bool            `json:"present"`
	Raw     json.RawMessage `json:"raw,omitempty"`
	Source  string          `json:"source,omitempty"`
}
type HistoryLocator struct {
	InvocationID            string   `json:"invocation_id"`
	GenerationOrdinal       *int64   `json:"generation_ordinal,omitempty"`
	CollectionInvocationIDs []string `json:"collection_invocation_ids,omitempty"`
	CollectionOrdinals      []int64  `json:"collection_ordinals,omitempty"`
	AcceptedOrdinal         *int64   `json:"accepted_ordinal,omitempty"`
	JobAttempt              int      `json:"job_attempt,omitempty"`
	TaskAttempt             int      `json:"task_attempt,omitempty"`
	Path                    string   `json:"path,omitempty"`
	StoryNodeID             string   `json:"story_node_id,omitempty"`
}

// HistoryActor preserves the recorded receipt without authenticating it anew.
// Provenance is submission_receipt for externally collected receipts,
// automation_receipt for known generated autofill/fallback receipts,
// reported_receipt for caller-supplied ordinary autofill metadata,
// reported_legacy for legacy UserID, or unavailable when neither was recorded.
// Consumers must not infer authentication from the receipt's actor kind alone.
type HistoryActor struct {
	Receipt      json.RawMessage `json:"receipt,omitempty"`
	LegacyUserID HistoryValue    `json:"legacy_user_id"`
	Provenance   string          `json:"provenance"`
}
type JobInputStoryEntry struct {
	EntryID          string                     `json:"entry_id"`
	RequestID        string                     `json:"request_id,omitempty"`
	Kind             string                     `json:"kind"`
	Title            HistoryValue               `json:"title"`
	Summary          HistoryValue               `json:"summary"`
	State            string                     `json:"state"`
	StateEvidence    []string                   `json:"state_evidence,omitempty"`
	RequestedAt      HistoryValue               `json:"requested_at"`
	Locator          HistoryLocator             `json:"locator"`
	FormFields       map[string]json.RawMessage `json:"form_fields"`
	AcceptedResponse HistoryValue               `json:"accepted_response"`
	ResponseSource   string                     `json:"response_source"`
	Actor            HistoryActor               `json:"actor"`
	Diagnostics      []HistoryDiagnostic        `json:"diagnostics,omitempty"`
}
type JobInputStoryPage struct {
	SnapshotRevision int64                `json:"snapshot_revision"`
	TenantID         string               `json:"tenant_id"`
	JobID            string               `json:"job_id"`
	Entries          []JobInputStoryEntry `json:"entries"`
	NextPageToken    string               `json:"next_page_token,omitempty"`
	SnapshotToken    string               `json:"snapshot_token"`
	ObservedAt       time.Time            `json:"observed_at"`
	Completeness     string               `json:"completeness"`
	Diagnostics      []HistoryDiagnostic  `json:"diagnostics,omitempty"`
}
type HistoryAssociation struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
}
type HistoryArtifactMember struct {
	OriginalRef      *artifacts.Ref       `json:"original_ref,omitempty"`
	ResolutionSource string               `json:"resolution_source,omitempty"`
	ArtifactID       string               `json:"artifact_id"`
	Ref              artifacts.Ref        `json:"ref"`
	Associations     []HistoryAssociation `json:"associations"`
	Availability     string               `json:"availability"`
	Reason           string               `json:"reason,omitempty"`
}
type HistoryActionability struct {
	Actionable  bool      `json:"actionable"`
	Observation string    `json:"observation"`
	ObservedAt  time.Time `json:"observed_at"`
	Reason      string    `json:"reason,omitempty"`
}
type JobInputStoryEntryDetail struct {
	SnapshotRevision int64 `json:"snapshot_revision"`
	JobInputStoryEntry
	RawForm           json.RawMessage         `json:"raw_form,omitempty"`
	RawAcceptedOutput json.RawMessage         `json:"raw_accepted_output,omitempty"`
	RequestManifest   []HistoryArtifactMember `json:"request_manifest"`
	ResponseManifest  []HistoryArtifactMember `json:"response_manifest"`
	SnapshotToken     string                  `json:"snapshot_token"`
	ObservedAt        time.Time               `json:"observed_at"`
	Completeness      string                  `json:"completeness"`
	Actionability     HistoryActionability    `json:"actionability"`
}
type HistoryArtifact struct {
	ArtifactID string
	Ref        artifacts.Ref
	Name       string
	SizeBytes  int64
	io.ReadCloser
}

// HistoryReader is deliberately read-only. SWFWorkflowControl satisfies this
// interface; neither a worker nor a recipe registry is needed.
type HistoryReader interface {
	GetJobRun(context.Context, jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error)
	GetWaitingTask(context.Context, jobdb.JobKey) (workflowctl.TaskHandle, error)
	GetArtifactLazy(context.Context, string, jobdb.ArtifactKey) jobdb.Artifact
}

type HistoryConfig struct {
	Reader HistoryReader
	// MapNode is an optional read-only lookup against an already available story.
	// Failure leaves history readable. It must not execute recipe operations.
	MapNode func(context.Context, jobdb.JobKey, HistoryLocator) (string, error)
	// SnapshotTTL defaults to five minutes. MaxSnapshots bounds cache entries,
	// never the number of occurrences in a job. Eviction explicitly expires tokens.
	SnapshotTTL  time.Duration
	MaxSnapshots int
}
