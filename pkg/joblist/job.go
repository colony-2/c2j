package joblist

import (
	"encoding/json"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Page contains one logical result page. Jobs is non-nil on a successful List.
type Page struct {
	Jobs          []Job  `json:"jobs"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

// Job is the CLI-compatible typed projection, with repository identity added.
// Detail fields remain for source compatibility but are never loaded by lists.
type Job struct {
	BlockingWaitSummary *jobdb.DependencyWaitSummary `json:"blocking_wait_summary,omitempty"`
	Metadata            json.RawMessage              `json:"metadata,omitempty"`
	SchemaHash          string                       `json:"schema_hash,omitempty"`
	ParentJobID         string                       `json:"parent_job_id,omitempty"`
	// Deprecated: not loaded in lists. Use explicit job inspection.
	BlockingWait *jobdb.DependencyWaitInfo `json:"blocking_wait,omitempty"`
	GoalID       string                    `json:"goal_id"`
	// Deprecated: not loaded in lists. Use explicit job inspection.
	OtherGoalIDs []string `json:"other_goal_ids"`
	// Completion fields are persisted terminal results, separate from scheduler Status.
	// Empty status means unavailable. Unknown statuses are preserved verbatim.
	CompletionStatus string `json:"completion_status,omitempty"`
	// Deprecated: not loaded in lists. Use explicit job inspection.
	CompletionDetail string         `json:"completion_detail,omitempty"`
	Execution        execution.View `json:"execution"`
	// Deprecated: not loaded in lists. Use explicit job inspection.
	ClientPayload         json.RawMessage `json:"client_payload,omitempty"`
	ClientPayloadRevision int64           `json:"client_payload_revision"`
	TenantID              string          `json:"tenant_id"`
	JobID                 string          `json:"job_id"`
	RepositorySource      string          `json:"repo,omitempty"`
	Prompt                string          `json:"prompt,omitempty"`
	Title                 string          `json:"title,omitempty"`
	Status                jobdb.JobStatus `json:"status"`
	Store                 jobdb.JobStore  `json:"store"`
	JobType               string          `json:"job_type"`
	CreatedAt             time.Time       `json:"created_at"`
	AvailableAt           time.Time       `json:"available_at"`
	ArchivedAt            *time.Time      `json:"archived_at,omitempty"`
	LeaseExpiresAt        *time.Time      `json:"lease_expires_at,omitempty"`
	ExpiresAt             *time.Time      `json:"expires_at,omitempty"`
	NextRoute             *jobdb.Route    `json:"next_route,omitempty"`
	// Deprecated: not loaded in lists. Use explicit job inspection.
	TaskWait *jobdb.TaskWait `json:"task_wait,omitempty"`
	// Deprecated: not loaded in lists. Use explicit job inspection.
	WaitFor         []string `json:"wait_for,omitempty"`
	CancelRequested bool     `json:"cancel_requested,omitempty"`
}

// CompactSummary also enforces the compact contract against older servers.
func CompactSummary(job jobdb.JobSummary) jobdb.JobSummary {
	job.BlockingWait = nil
	job.OtherGoalIDs = nil
	job.CompletionDetail = ""
	job.ClientPayload = nil
	job.ExecutionState = jobdb.ExecutionState{}
	job.WaitFor = nil
	return job
}

// CloneBlockingWaitSummary copies the compact wait and its optional deadline.
func CloneBlockingWaitSummary(wait *jobdb.DependencyWaitSummary) *jobdb.DependencyWaitSummary {
	if wait == nil {
		return nil
	}
	copy := *wait
	copy.Deadline = cloneTime(wait.Deadline)
	return &copy
}

// JobFromSummary applies c2j's execution interpretation without resolving a
// recipe or reading history. Malformed execution data stays an item diagnostic.
func JobFromSummary(job jobdb.JobSummary) Job {
	var meta struct {
		RepositorySource string `json:"repo"`
		Prompt           string `json:"prompt"`
		Title            string `json:"title"`
	}
	// Unrelated metadata does not prevent projecting job state. ExecutionView
	// independently reports malformed execution metadata for waiting jobs.
	_ = json.Unmarshal(job.Metadata, &meta)
	return Job{
		BlockingWaitSummary: CloneBlockingWaitSummary(job.BlockingWaitSummary),
		Metadata:            append(json.RawMessage(nil), job.Metadata...),
		SchemaHash:          job.SchemaHash,
		ParentJobID:         job.ParentJobID,
		Prompt:              meta.Prompt,
		Title:               meta.Title,
		GoalID:              job.GoalID,
		CompletionStatus:    job.CompletionStatus,
		Execution:           ExecutionView(job), RepositorySource: meta.RepositorySource,
		ClientPayloadRevision: job.ClientPayloadRevision,
		TenantID:              job.JobKey.TenantId, JobID: job.JobKey.JobId, Status: job.Status,
		Store: StoreForJob(job), JobType: job.JobType, CreatedAt: job.CreatedAt, AvailableAt: job.AvailableAt,
		ArchivedAt: cloneTime(job.ArchivedAt), LeaseExpiresAt: cloneTime(job.LeaseExpiresAt), ExpiresAt: cloneTime(job.ExpiresAt),
		NextRoute:       jobdb.CloneRoute(job.NextRoute),
		CancelRequested: job.CancelRequested,
	}
}
