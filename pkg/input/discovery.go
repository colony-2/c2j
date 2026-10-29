package input

import (
	"context"
	"errors"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type PendingInputOptions struct {
	PageSize  int    `json:"page_size,omitempty"`
	PageToken string `json:"page_token,omitempty"`
}

// PendingInputOccurrence contains identity and timing only. Fetch GetForm for
// questions, kind, title, or documents. RequestID/RequestedAt may be absent on
// legacy ordinary forms; JobID + TaskOrdinal still identifies their occurrence.
type PendingInputOccurrence struct {
	JobID       string `json:"job_id"`
	TaskOrdinal int64  `json:"task_ordinal"`
	RequestID   string `json:"request_id,omitempty"`
	RequestedAt string `json:"requested_at,omitempty"`
}
type PendingInputPage struct {
	Inputs        []PendingInputOccurrence `json:"inputs"`
	NextPageToken string                   `json:"next_page_token,omitempty"`
}

func (r *Runtime) pendingJobs(ctx context.Context, tenantID string, options PendingInputOptions) ([]workflowctl.JobItem, string, error) {
	if tenantID == "" {
		return nil, "", validationError("tenant_id", "is required")
	}
	if options.PageSize < 0 {
		return nil, "", validationError("page_size", "must not be negative")
	}
	jobs, next, err := r.ctl.ListJobs(ctx, jobdb.ListJobsRequest{
		Stores: []jobdb.JobStore{jobdb.JobStoreActive}, TenantIds: []string{tenantID},
		Statuses: []jobdb.JobStatus{jobdb.JobStatusReady},
		JobTasks: []jobdb.JobTaskFilter{{JobType: "recipe", TaskType: "input:collect_user_input"}},
		PageSize: options.PageSize, PageToken: options.PageToken,
	})
	if err != nil {
		return nil, "", runtimeError(ErrStorage, "list pending inputs", err)
	}
	return jobs, next, nil
}

// ListPendingInputsPage lists a live page, not a snapshot. Occurrences completed
// during reading are skipped; replacement forms use the loaded task identity.
// An empty page with a continuation token is not the end of the list.
// Form data is read only within this page;
// document bytes are never loaded for discovery.
func (r *Runtime) ListPendingInputsPage(ctx context.Context, tenantID string, options PendingInputOptions) (PendingInputPage, error) {
	jobs, next, err := r.pendingJobs(ctx, tenantID, options)
	if err != nil {
		return PendingInputPage{}, err
	}
	page := PendingInputPage{Inputs: []PendingInputOccurrence{}, NextPageToken: next}
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return PendingInputPage{}, err
		}
		if job.JobKey.TenantId != tenantID {
			continue
		}
		task, out, _, err := r.getOutput(ctx, tenantID, job.JobKey.JobId)
		if errors.Is(err, ErrInputNotPending) {
			continue
		}
		if err != nil {
			return PendingInputPage{}, err
		}
		var form InputForm
		if err := ops.DecodeWithJsonTags(out.OpOutput, &form); err != nil {
			return PendingInputPage{}, invalidFormError(err)
		}
		page.Inputs = append(page.Inputs, PendingInputOccurrence{JobID: job.JobKey.JobId, TaskOrdinal: task.TaskOrdinalToComplete(), RequestID: form.RequestID, RequestedAt: form.RequestedAt})
	}
	return page, nil
}
