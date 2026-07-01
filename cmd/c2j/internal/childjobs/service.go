package childjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/recipejob"
)

func Run(ctx context.Context, opts Options) error {
	if err := opts.Complete(ctx); err != nil {
		return err
	}
	if err := opts.Validate(); err != nil {
		return err
	}

	statuses, err := parseJobStatuses(opts.Statuses)
	if err != nil {
		return err
	}
	createdAfter, err := parseOptionalTime(opts.CreatedAfter)
	if err != nil {
		return err
	}
	createdBefore, err := parseOptionalTime(opts.CreatedBefore)
	if err != nil {
		return err
	}

	handle, err := swfruntime.Open(ctx, opts.SWFURL)
	if err != nil {
		return fmt.Errorf("open JobDB runtime: %w", err)
	}
	defer handle.Cleanup()

	request := recipejob.ListChildRecipeJobsRequest{
		TenantID:             opts.TenantID,
		ParentTenantID:       opts.ParentTenantID,
		ParentJobID:          opts.ParentJobID,
		ParentInvocationHash: opts.ParentInvocationHash,
		AllParentInvocations: opts.AllParentInvocations,
		Statuses:             statuses,
		Stores:               recipejob.StoresForStatuses(statuses),
		CreatedAfter:         createdAfter,
		CreatedBefore:        createdBefore,
		PageSize:             opts.PageSize,
		PageToken:            strings.TrimSpace(opts.PageToken),
	}

	jobs := make([]recipejob.RecipeJob, 0)
	nextPageToken := ""
	for {
		resp, err := recipejob.ListChildRecipeJobs(ctx, handle.Engine, request)
		if err != nil {
			return fmt.Errorf("list child jobs: %w", err)
		}
		jobs = append(jobs, resp.Jobs...)
		nextPageToken = resp.NextPageToken
		if !opts.All || strings.TrimSpace(resp.NextPageToken) == "" {
			break
		}
		request.PageToken = resp.NextPageToken
	}

	result := recipejob.ListRecipeJobsResponse{
		Jobs:          jobs,
		NextPageToken: nextPageToken,
	}
	if opts.JSONOutput {
		return json.NewEncoder(opts.Stdout).Encode(result)
	}
	if len(jobs) == 0 {
		_, err := fmt.Fprintln(opts.Stdout, "no child jobs found")
		return err
	}

	w := tabwriter.NewWriter(opts.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "JOB ID\tSTATUS\tSTORE\tRECIPE\tCREATED\tPARENT OP"); err != nil {
		return err
	}
	for _, job := range jobs {
		if _, err := fmt.Fprintf(
			w,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			job.JobID,
			job.Status,
			job.Store,
			job.RecipeName,
			job.CreatedAt.Format(time.RFC3339),
			parentOp(job),
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

func parentOp(job recipejob.RecipeJob) string {
	if job.Parent == nil {
		return ""
	}
	if strings.TrimSpace(job.Parent.OpStep) == "" {
		return job.Parent.OpType
	}
	if strings.TrimSpace(job.Parent.OpType) == "" {
		return job.Parent.OpStep
	}
	return job.Parent.OpType + ":" + job.Parent.OpStep
}
