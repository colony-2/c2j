package listjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	configpkg "github.com/colony-2/c2j/pkg/config"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/swf-go/pkg/swf"
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

func buildRequest(ctx context.Context, opts Options) (swf.ListJobsRequest, error) {
	statuses, err := parseJobStatuses(opts.Statuses)
	if err != nil {
		return swf.ListJobsRequest{}, err
	}
	waitingFor, err := parseWaitingForFilters(opts.WaitingFor)
	if err != nil {
		return swf.ListJobsRequest{}, err
	}
	createdAfter, err := parseOptionalTime(opts.CreatedAfter)
	if err != nil {
		return swf.ListJobsRequest{}, err
	}
	createdBefore, err := parseOptionalTime(opts.CreatedBefore)
	if err != nil {
		return swf.ListJobsRequest{}, err
	}
	cellName, err := resolveListCellName(ctx, opts)
	if err != nil {
		return swf.ListJobsRequest{}, err
	}
	metadataFilter, err := swf.Metadata().EqualFilter(starter.MetaFieldCellName, cellName)
	if err != nil {
		return swf.ListJobsRequest{}, err
	}

	jobIDs := make([]swf.JobKey, 0, len(opts.JobIDs))
	for _, value := range opts.JobIDs {
		for _, part := range splitCSV(value) {
			jobIDs = append(jobIDs, swf.JobKey{
				TenantId: opts.TenantID,
				JobId:    part,
			})
		}
	}

	jobTypes := make([]string, 0, len(opts.JobTypes))
	for _, value := range opts.JobTypes {
		jobTypes = append(jobTypes, splitCSV(value)...)
	}

	return swf.ListJobsRequest{
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

func makeJobRow(job swf.JobSummary) jobRow {
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

func storeForJob(job swf.JobSummary) swf.JobStore {
	if job.ArchivedAt != nil {
		return swf.JobStoreArchived
	}
	return swf.JobStoreActive
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

func resolveListCellName(ctx context.Context, opts Options) (string, error) {
	if strings.TrimSpace(opts.Cell) == "" || opts.Self {
		return resolveSelfCellName(ctx, opts.WorkingDir)
	}
	return resolveExplicitCellName(ctx, opts.WorkingDir, opts.Cell)
}

func resolveSelfCellName(ctx context.Context, workingDir string) (string, error) {
	cfg, err := configpkg.LoadProjectConfig(workingDir)
	if err != nil {
		return "", fmt.Errorf("resolve current cell: %w", err)
	}

	selfRepo, err := cfg.SelfRepo(ctx)
	if err != nil {
		return "", err
	}
	selfRepo = strings.TrimSpace(selfRepo)
	if selfRepo == "" {
		return "", fmt.Errorf("resolve current cell: self.repo is empty")
	}
	return cellNameFromRepo(ctx, cfg, selfRepo)
}

func resolveExplicitCellName(ctx context.Context, workingDir string, cell string) (string, error) {
	cfg, cfgErr := configpkg.LoadProjectConfig(workingDir)
	if cfgErr != nil && cfgErr != configpkg.ErrConfigNotFound {
		return "", cfgErr
	}

	resolvedRepo, err := resolveCellInput(ctx, cfg, workingDir, cell)
	if err != nil {
		return "", err
	}

	return cellNameFromRepo(ctx, cfg, resolvedRepo)
}

func cellNameFromRepo(ctx context.Context, cfg *configpkg.ProjectConfig, repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", fmt.Errorf("cell repo is empty")
	}

	if cfg != nil {
		if name, ok := cfg.CellNameFromRepo(ctx, repo); ok && strings.TrimSpace(name) != "" {
			return name, nil
		}
	}

	name := compiler.RepositoryNameFromSource(repo)
	if name == "" {
		if normalized, err := compiler.NormalizeGitRepositorySource(repo); err == nil {
			name = compiler.RepositoryNameFromSource(normalized)
		}
	}
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("could not derive a cell name from %q", repo)
	}
	return name, nil
}

func resolveCellInput(ctx context.Context, cfg *configpkg.ProjectConfig, workingDir string, value string) (string, error) {
	if cfg != nil {
		return cfg.ExpandCellName(ctx, value)
	}
	return resolveRepositoryInput(workingDir, value)
}

func resolveRepositoryInput(workingDir string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("cell value is required")
	}
	if compiler.IsGitRecipeSelector(value) {
		return "", fmt.Errorf("cell %q must be a git repository, not a recipe selector", value)
	}
	if strings.Contains(value, "://") || strings.HasPrefix(value, "git@") {
		return value, nil
	}
	if filepath.IsAbs(value) || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") {
		return absPathFromWorkingDir(workingDir, value)
	}
	if strings.Contains(value, "/") || strings.Contains(value, ":") {
		return value, nil
	}
	return "", fmt.Errorf("cell %q looks like a short name; define pattern in .c2j/config.yaml or use an explicit repo/path", value)
}

func absPathFromWorkingDir(workingDir string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(value) {
		return value, nil
	}
	return filepath.Abs(filepath.Join(workingDir, value))
}
