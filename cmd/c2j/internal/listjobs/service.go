package listjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type jobRow struct {
	TenantID        string     `json:"tenant_id"`
	JobID           string     `json:"job_id"`
	Status          string     `json:"status"`
	Store           string     `json:"store"`
	JobType         string     `json:"job_type"`
	CreatedAt       time.Time  `json:"created_at"`
	AvailableAt     time.Time  `json:"available_at"`
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	NextNeed        string     `json:"next_need,omitempty"`
	TaskWaitNext    string     `json:"task_wait_next,omitempty"`
	TaskWaitInput   *int64     `json:"task_wait_input,omitempty"`
	TaskWaitOutput  *int64     `json:"task_wait_output,omitempty"`
	WaitFor         []string   `json:"wait_for,omitempty"`
	CancelRequested bool       `json:"cancel_requested,omitempty"`
}

type listResult struct {
	Jobs          []jobRow `json:"jobs"`
	NextPageToken string   `json:"next_page_token,omitempty"`
}

func Run(ctx context.Context, opts Options) error {
	if err := opts.Complete(ctx); err != nil {
		return err
	}
	if err := opts.Validate(); err != nil {
		return err
	}

	request, err := buildRequest(ctx, opts)
	if err != nil {
		return err
	}

	handle, err := swfruntime.Open(ctx, opts.SWFURL)
	if err != nil {
		return fmt.Errorf("open SWF runtime: %w", err)
	}
	defer handle.Cleanup()

	rows := make([]jobRow, 0)
	nextPageToken := ""
	for {
		resp, err := handle.Engine.ListJobs(ctx, request)
		if err != nil {
			return fmt.Errorf("list jobs: %w", err)
		}

		for _, job := range resp.Jobs {
			rows = append(rows, makeJobRow(job))
		}
		nextPageToken = resp.NextPageToken

		if !opts.All || strings.TrimSpace(resp.NextPageToken) == "" {
			break
		}
		request.PageToken = resp.NextPageToken
	}

	if opts.JSONOutput {
		return json.NewEncoder(opts.Stdout).Encode(listResult{
			Jobs:          rows,
			NextPageToken: nextPageToken,
		})
	}

	if len(rows) == 0 {
		_, err := fmt.Fprintln(opts.Stdout, "no jobs found")
		return err
	}

	w := tabwriter.NewWriter(opts.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "JOB ID\tSTATUS\tSTORE\tTYPE\tCREATED\tAVAILABLE\tNEXT"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(
			w,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.JobID,
			row.Status,
			row.Store,
			row.JobType,
			row.CreatedAt.Format(time.RFC3339),
			row.AvailableAt.Format(time.RFC3339),
			displayNext(row),
		); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if strings.TrimSpace(nextPageToken) == "" {
		return nil
	}
	_, err = fmt.Fprintf(opts.Stdout, "\nnext_page_token: %s\n", nextPageToken)
	return err
}

func buildRequest(ctx context.Context, opts Options) (jobdb.ListJobsRequest, error) {
	statuses, err := parseJobStatuses(opts.Statuses)
	if err != nil {
		return jobdb.ListJobsRequest{}, err
	}
	waitingFor, err := parseWaitingForFilters(opts.WaitingFor)
	if err != nil {
		return jobdb.ListJobsRequest{}, err
	}
	createdAfter, err := parseOptionalTime(opts.CreatedAfter)
	if err != nil {
		return jobdb.ListJobsRequest{}, err
	}
	createdBefore, err := parseOptionalTime(opts.CreatedBefore)
	if err != nil {
		return jobdb.ListJobsRequest{}, err
	}
	target, err := recipejob.ResolveTarget(ctx, recipejob.ResolveTargetRequest{
		WorkingDir: opts.WorkingDir,
		Cell:       opts.Cell,
		Self:       opts.Self,
		TenantID:   opts.TenantID,
	})
	if err != nil {
		return jobdb.ListJobsRequest{}, err
	}
	metadataFilter, err := jobdb.Metadata().EqualFilter(starter.MetaFieldRepo, target.RepositorySource)
	if err != nil {
		return jobdb.ListJobsRequest{}, err
	}

	jobIDs := make([]jobdb.JobKey, 0, len(opts.JobIDs))
	for _, value := range opts.JobIDs {
		for _, part := range splitCSV(value) {
			jobIDs = append(jobIDs, jobdb.JobKey{
				TenantId: opts.TenantID,
				JobId:    part,
			})
		}
	}

	jobTypes := make([]string, 0, len(opts.JobTypes))
	for _, value := range opts.JobTypes {
		jobTypes = append(jobTypes, splitCSV(value)...)
	}

	return jobdb.ListJobsRequest{
		TenantIds:      []string{opts.TenantID},
		Statuses:       statuses,
		Stores:         storesForStatuses(statuses),
		JobTypes:       jobTypes,
		JobTasks:       waitingFor,
		JobKeys:        jobIDs,
		MetadataFilter: metadataFilter,
		CreatedAfter:   createdAfter,
		CreatedBefore:  createdBefore,
		PageSize:       opts.PageSize,
		PageToken:      strings.TrimSpace(opts.PageToken),
	}, nil
}

func makeJobRow(job jobdb.JobSummary) jobRow {
	nextNeed := ""
	if job.NextNeed != nil {
		nextNeed = *job.NextNeed
	}
	taskWaitNext := ""
	if job.TaskWaitNext != nil {
		taskWaitNext = *job.TaskWaitNext
	}

	return jobRow{
		TenantID:        job.JobKey.TenantId,
		JobID:           job.JobKey.JobId,
		Status:          string(job.Status),
		Store:           string(storeForJob(job)),
		JobType:         job.JobType,
		CreatedAt:       job.CreatedAt,
		AvailableAt:     job.AvailableAt,
		ArchivedAt:      job.ArchivedAt,
		LeaseExpiresAt:  job.LeaseExpiresAt,
		ExpiresAt:       job.ExpiresAt,
		NextNeed:        nextNeed,
		TaskWaitNext:    taskWaitNext,
		TaskWaitInput:   job.TaskWaitInput,
		TaskWaitOutput:  job.TaskWaitOutput,
		WaitFor:         append([]string(nil), job.WaitFor...),
		CancelRequested: job.CancelRequested,
	}
}

func storeForJob(job jobdb.JobSummary) jobdb.JobStore {
	return recipejob.StoreForJob(job)
}

func displayNext(row jobRow) string {
	switch {
	case strings.TrimSpace(row.TaskWaitNext) != "":
		return row.TaskWaitNext
	case strings.TrimSpace(row.NextNeed) != "":
		return row.NextNeed
	case len(row.WaitFor) > 0:
		return strings.Join(row.WaitFor, ",")
	default:
		return ""
	}
}
