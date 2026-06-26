package recipejob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

var ErrJobNotFound = errors.New("recipe job not found")

type Lister interface {
	ListJobs(ctx context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error)
}

type ListRecipeJobsRequest struct {
	TenantID         string
	RepositorySource string
	Statuses         []jobdb.JobStatus
	Stores           []jobdb.JobStore
	JobTasks         []jobdb.JobTaskFilter
	JobIDs           []string
	JobKeys          []jobdb.JobKey
	MetadataFilter   jobdb.MetadataFilter
	CreatedAfter     *time.Time
	CreatedBefore    *time.Time
	PageSize         int
	PageToken        string
}

type ListRecipeJobsResponse struct {
	Jobs          []RecipeJob `json:"jobs"`
	NextPageToken string      `json:"next_page_token,omitempty"`
}

type GetRecipeJobRequest struct {
	TenantID string
	JobID    string
}

type RecipeJob struct {
	TenantID         string          `json:"tenant_id"`
	JobID            string          `json:"job_id"`
	Status           jobdb.JobStatus `json:"status"`
	Store            jobdb.JobStore  `json:"store"`
	JobType          string          `json:"job_type"`
	RecipeName       string          `json:"recipe"`
	RepositorySource string          `json:"repo,omitempty"`
	CellID           string          `json:"cell_id,omitempty"`
	CellName         string          `json:"cell_name,omitempty"`
	GitRef           string          `json:"git_ref,omitempty"`
	InputHash        string          `json:"input_hash,omitempty"`
	SubmittedAt      *time.Time      `json:"submitted_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	AvailableAt      time.Time       `json:"available_at"`
	ArchivedAt       *time.Time      `json:"archived_at,omitempty"`
	LeaseExpiresAt   *time.Time      `json:"lease_expires_at,omitempty"`
	ExpiresAt        *time.Time      `json:"expires_at,omitempty"`
	NextNeed         string          `json:"next_need,omitempty"`
	TaskWaitNext     string          `json:"task_wait_next,omitempty"`
	TaskWaitInput    *int64          `json:"task_wait_input,omitempty"`
	TaskWaitOutput   *int64          `json:"task_wait_output,omitempty"`
	WaitFor          []string        `json:"wait_for,omitempty"`
	CancelRequested  bool            `json:"cancel_requested,omitempty"`
}

func BuildListJobsRequest(req ListRecipeJobsRequest) (jobdb.ListJobsRequest, error) {
	tenantID := strings.TrimSpace(req.TenantID)
	if tenantID == "" {
		return jobdb.ListJobsRequest{}, fmt.Errorf("tenant ID is required")
	}

	metadataFilter := req.MetadataFilter
	repositorySource := strings.TrimSpace(req.RepositorySource)
	if repositorySource != "" {
		normalized, err := compiler.NormalizeGitRepositorySource(repositorySource)
		if err != nil {
			return jobdb.ListJobsRequest{}, err
		}
		repoFilter, err := jobdb.Metadata().EqualFilter(starter.MetaFieldRepo, normalized)
		if err != nil {
			return jobdb.ListJobsRequest{}, err
		}
		if metadataFilter == nil {
			metadataFilter = repoFilter
		} else {
			metadataFilter, err = metadataFilter.AndFilter(repoFilter)
			if err != nil {
				return jobdb.ListJobsRequest{}, err
			}
		}
	}

	jobKeys := make([]jobdb.JobKey, 0, len(req.JobKeys)+len(req.JobIDs))
	for _, key := range req.JobKeys {
		if strings.TrimSpace(key.TenantId) == "" {
			key.TenantId = tenantID
		}
		jobKeys = append(jobKeys, key)
	}
	for _, jobID := range req.JobIDs {
		jobID = strings.TrimSpace(jobID)
		if jobID == "" {
			continue
		}
		jobKeys = append(jobKeys, jobdb.JobKey{TenantId: tenantID, JobId: jobID})
	}

	return jobdb.ListJobsRequest{
		TenantIds:      []string{tenantID},
		Statuses:       append([]jobdb.JobStatus(nil), req.Statuses...),
		Stores:         append([]jobdb.JobStore(nil), req.Stores...),
		JobTypes:       []string{starter.RecipeJobType},
		JobTasks:       append([]jobdb.JobTaskFilter(nil), req.JobTasks...),
		JobKeys:        jobKeys,
		MetadataFilter: metadataFilter,
		CreatedAfter:   req.CreatedAfter,
		CreatedBefore:  req.CreatedBefore,
		PageSize:       req.PageSize,
		PageToken:      strings.TrimSpace(req.PageToken),
	}, nil
}

func ListRecipeJobs(ctx context.Context, lister Lister, req ListRecipeJobsRequest) (ListRecipeJobsResponse, error) {
	if lister == nil {
		return ListRecipeJobsResponse{}, fmt.Errorf("job lister is required")
	}

	listReq, err := BuildListJobsRequest(req)
	if err != nil {
		return ListRecipeJobsResponse{}, err
	}
	resp, err := lister.ListJobs(ctx, listReq)
	if err != nil {
		return ListRecipeJobsResponse{}, err
	}

	jobs := make([]RecipeJob, 0, len(resp.Jobs))
	for _, summary := range resp.Jobs {
		job, ok, err := RecipeJobFromSummary(summary)
		if err != nil {
			return ListRecipeJobsResponse{}, err
		}
		if ok {
			jobs = append(jobs, job)
		}
	}

	return ListRecipeJobsResponse{
		Jobs:          jobs,
		NextPageToken: resp.NextPageToken,
	}, nil
}

func GetRecipeJob(ctx context.Context, lister Lister, req GetRecipeJobRequest) (RecipeJob, error) {
	jobID := strings.TrimSpace(req.JobID)
	if jobID == "" {
		return RecipeJob{}, fmt.Errorf("job ID is required")
	}
	resp, err := ListRecipeJobs(ctx, lister, ListRecipeJobsRequest{
		TenantID: req.TenantID,
		JobIDs:   []string{jobID},
		Stores:   []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived},
		PageSize: 1,
	})
	if err != nil {
		return RecipeJob{}, err
	}
	if len(resp.Jobs) == 0 {
		return RecipeJob{}, ErrJobNotFound
	}
	return resp.Jobs[0], nil
}

func RecipeJobFromSummary(summary jobdb.JobSummary) (RecipeJob, bool, error) {
	if strings.TrimSpace(summary.JobType) != "" && summary.JobType != starter.RecipeJobType {
		return RecipeJob{}, false, nil
	}

	meta, err := JobMetadataFromRaw(summary.Metadata)
	if err != nil {
		return RecipeJob{}, false, err
	}
	start, err := startJobFromRaw(summary.Payload)
	if err != nil {
		return RecipeJob{}, false, err
	}

	nextNeed := ""
	if summary.NextNeed != nil {
		nextNeed = *summary.NextNeed
	}
	taskWaitNext := ""
	if summary.TaskWaitNext != nil {
		taskWaitNext = *summary.TaskWaitNext
	}

	job := RecipeJob{
		TenantID:        summary.JobKey.TenantId,
		JobID:           summary.JobKey.JobId,
		Status:          summary.Status,
		Store:           StoreForJob(summary),
		JobType:         summary.JobType,
		CreatedAt:       summary.CreatedAt,
		AvailableAt:     summary.AvailableAt,
		ArchivedAt:      summary.ArchivedAt,
		LeaseExpiresAt:  summary.LeaseExpiresAt,
		ExpiresAt:       summary.ExpiresAt,
		NextNeed:        nextNeed,
		TaskWaitNext:    taskWaitNext,
		TaskWaitInput:   summary.TaskWaitInput,
		TaskWaitOutput:  summary.TaskWaitOutput,
		WaitFor:         append([]string(nil), summary.WaitFor...),
		CancelRequested: summary.CancelRequested,
	}
	if job.JobType == "" {
		job.JobType = starter.RecipeJobType
	}

	if meta != nil {
		job.RecipeName = meta.RecipeName
		job.CellID = meta.CellID
		job.CellName = meta.CellName
		job.RepositorySource = meta.RepositorySource
		job.GitRef = meta.GitRef
	}
	if start != nil {
		if strings.TrimSpace(job.RecipeName) == "" {
			job.RecipeName = start.RecipeName
		}
		if strings.TrimSpace(job.CellID) == "" {
			job.CellID = start.JobContext.Workflow.CellID
		}
		if strings.TrimSpace(job.CellName) == "" {
			job.CellName = start.JobContext.Workflow.CellName
		}
		if strings.TrimSpace(job.RepositorySource) == "" {
			job.RepositorySource = start.JobContext.GitBase.BaseRepo
		}
		if strings.TrimSpace(job.RepositorySource) == "" {
			job.RepositorySource = start.JobContext.RecipeSource.Repo
		}
		if strings.TrimSpace(job.GitRef) == "" {
			job.GitRef = start.GitRef
		}
		if strings.TrimSpace(job.GitRef) == "" {
			job.GitRef = start.JobContext.GitBase.BaseRef
		}
		if strings.TrimSpace(job.GitRef) == "" {
			job.GitRef = start.JobContext.RecipeSource.Ref
		}
		job.InputHash = start.InputHash
		job.SubmittedAt = start.SubmittedAt
	}
	if normalized, err := compiler.NormalizeGitRepositorySource(job.RepositorySource); err == nil {
		job.RepositorySource = normalized
	}

	return job, true, nil
}

func JobMetadataFromRaw(raw json.RawMessage) (*starter.JobMetadata, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var meta starter.JobMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func DefaultVisibleStatuses() []jobdb.JobStatus {
	return []jobdb.JobStatus{
		jobdb.JobStatusReady,
		jobdb.JobStatusExpired,
		jobdb.JobStatusPendingJobs,
		jobdb.JobStatusAwaitingFuture,
		jobdb.JobStatusActive,
		jobdb.JobStatusCrashConcern,
	}
}

func StoresForStatuses(statuses []jobdb.JobStatus) []jobdb.JobStore {
	if len(statuses) == 0 {
		return nil
	}
	hasActive := false
	hasArchived := false
	for _, status := range statuses {
		switch status {
		case jobdb.JobStatusCancelled, jobdb.JobStatusCompleted:
			hasArchived = true
		default:
			hasActive = true
		}
	}

	switch {
	case hasActive && hasArchived:
		return []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}
	case hasArchived:
		return []jobdb.JobStore{jobdb.JobStoreArchived}
	default:
		return []jobdb.JobStore{jobdb.JobStoreActive}
	}
}

func StoreForJob(job jobdb.JobSummary) jobdb.JobStore {
	if job.ArchivedAt != nil {
		return jobdb.JobStoreArchived
	}
	return jobdb.JobStoreActive
}

func startJobFromRaw(raw json.RawMessage) (*workflowctl.StartJob, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var start workflowctl.StartJob
	if err := json.Unmarshal(raw, &start); err != nil {
		return nil, err
	}
	return &start, nil
}
