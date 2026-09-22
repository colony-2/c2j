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
// ClientPayload is optional raw data; execution consumers should use Execution.
type Job struct {
	Execution             execution.View  `json:"execution"`
	ClientPayload         json.RawMessage `json:"client_payload,omitempty"`
	ClientPayloadRevision int64           `json:"client_payload_revision"`
	TenantID              string          `json:"tenant_id"`
	JobID                 string          `json:"job_id"`
	RepositorySource      string          `json:"repo,omitempty"`
	Status                jobdb.JobStatus `json:"status"`
	Store                 jobdb.JobStore  `json:"store"`
	JobType               string          `json:"job_type"`
	CreatedAt             time.Time       `json:"created_at"`
	AvailableAt           time.Time       `json:"available_at"`
	ArchivedAt            *time.Time      `json:"archived_at,omitempty"`
	LeaseExpiresAt        *time.Time      `json:"lease_expires_at,omitempty"`
	ExpiresAt             *time.Time      `json:"expires_at,omitempty"`
	NextRoute             *jobdb.Route    `json:"next_route,omitempty"`
	TaskWait              *jobdb.TaskWait `json:"task_wait,omitempty"`
	WaitFor               []string        `json:"wait_for,omitempty"`
	CancelRequested       bool            `json:"cancel_requested,omitempty"`
}

// JobFromSummary applies c2j's execution interpretation without resolving a
// recipe or reading history. Malformed execution data stays an item diagnostic.
func JobFromSummary(job jobdb.JobSummary) Job {
	var meta struct {
		RepositorySource string `json:"repo"`
	}
	// Unrelated metadata does not prevent projecting job state. ExecutionView
	// independently reports malformed execution metadata for waiting jobs.
	_ = json.Unmarshal(job.Metadata, &meta)
	return Job{
		Execution: ExecutionView(job), RepositorySource: meta.RepositorySource,
		ClientPayload:         append(json.RawMessage(nil), job.ClientPayload...),
		ClientPayloadRevision: job.ClientPayloadRevision,
		TenantID:              job.JobKey.TenantId, JobID: job.JobKey.JobId, Status: job.Status,
		Store: StoreForJob(job), JobType: job.JobType, CreatedAt: job.CreatedAt, AvailableAt: job.AvailableAt,
		ArchivedAt: cloneTime(job.ArchivedAt), LeaseExpiresAt: cloneTime(job.LeaseExpiresAt), ExpiresAt: cloneTime(job.ExpiresAt),
		NextRoute: jobdb.CloneRoute(job.NextRoute), TaskWait: jobdb.CloneExecutionState(job.ExecutionState).TaskWait,
		WaitFor: append([]string(nil), job.WaitFor...), CancelRequested: job.CancelRequested,
	}
}
