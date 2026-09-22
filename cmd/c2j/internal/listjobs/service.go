package listjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/jobutil"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type jobRow = joblist.Job
type listResult = joblist.Page

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
		return fmt.Errorf("open JobDB runtime: %w", err)
	}
	defer handle.Cleanup()

	rows := make([]jobRow, 0)
	nextPageToken := ""
	for {
		resp, err := joblist.ListExecutionJobs(ctx, handle.Engine, request, opts.ExecutionFilter)
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
	var jobIDs []string
	for _, value := range opts.JobIDs {
		jobIDs = append(jobIDs, splitCSV(value)...)
	}
	return joblist.BuildRequest(opts.TenantID, joblist.Query{
		Repository: target.RepositorySource, Statuses: statuses, JobTypes: opts.JobTypes,
		JobTasks: waitingFor, JobIDs: jobIDs, CreatedAfter: createdAfter, CreatedBefore: createdBefore,
		PageSize: opts.PageSize, PageToken: opts.PageToken, ExecutionFilter: opts.ExecutionFilter,
	})
}

func makeJobRow(job jobdb.JobSummary) jobRow { return joblist.JobFromSummary(job) }

func storeForJob(job jobdb.JobSummary) jobdb.JobStore {
	return recipejob.StoreForJob(job)
}

func displayNext(row jobRow) string {
	switch {
	case row.NextRoute != nil:
		return jobutil.FormatRoute(*row.NextRoute)
	case len(row.WaitFor) > 0:
		return strings.Join(row.WaitFor, ",")
	default:
		return ""
	}
}
