package recipejob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/jobcontext"
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

type ListChildRecipeJobsRequest struct {
	TenantID             string
	ParentTenantID       string
	ParentJobID          string
	ParentInvocationHash string
	AllParentInvocations bool
	Statuses             []jobdb.JobStatus
	Stores               []jobdb.JobStore
	CreatedAfter         *time.Time
	CreatedBefore        *time.Time
	PageSize             int
	PageToken            string
}

type GetRecipeJobRequest struct {
	TenantID string
	JobID    string
}

type RecipeJob struct {
	TenantID         string             `json:"tenant_id"`
	JobID            string             `json:"job_id"`
	Status           jobdb.JobStatus    `json:"status"`
	Store            jobdb.JobStore     `json:"store"`
	JobType          string             `json:"job_type"`
	RecipeName       string             `json:"recipe"`
	RepositorySource string             `json:"repo,omitempty"`
	CellID           string             `json:"cell_id,omitempty"`
	CellName         string             `json:"cell_name,omitempty"`
	GitRef           string             `json:"git_ref,omitempty"`
	InputHash        string             `json:"input_hash,omitempty"`
	Parent           *jobcontext.Parent `json:"parent,omitempty"`
	SubmittedAt      *time.Time         `json:"submitted_at,omitempty"`
	CreatedAt        time.Time          `json:"created_at"`
	AvailableAt      time.Time          `json:"available_at"`
	ArchivedAt       *time.Time         `json:"archived_at,omitempty"`
	LeaseExpiresAt   *time.Time         `json:"lease_expires_at,omitempty"`
	ExpiresAt        *time.Time         `json:"expires_at,omitempty"`
	NextNeed         string             `json:"next_need,omitempty"`
	TaskWaitNext     string             `json:"task_wait_next,omitempty"`
	TaskWaitInput    *int64             `json:"task_wait_input,omitempty"`
	TaskWaitOutput   *int64             `json:"task_wait_output,omitempty"`
	WaitFor          []string           `json:"wait_for,omitempty"`
	CancelRequested  bool               `json:"cancel_requested,omitempty"`
}

type workflowLister interface {
	ListJobs(ctx context.Context, request jobdb.ListJobsRequest) (jobs []workflowctl.JobItem, nextPage string, err error)
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

func BuildListChildJobsRequest(req ListChildRecipeJobsRequest) (jobdb.ListJobsRequest, error) {
	tenantID := strings.TrimSpace(req.TenantID)
	if tenantID == "" {
		return jobdb.ListJobsRequest{}, fmt.Errorf("tenant ID is required")
	}
	parentTenantID := strings.TrimSpace(req.ParentTenantID)
	if parentTenantID == "" {
		return jobdb.ListJobsRequest{}, fmt.Errorf("parent tenant ID is required")
	}
	parentJobID := strings.TrimSpace(req.ParentJobID)
	if parentJobID == "" {
		return jobdb.ListJobsRequest{}, fmt.Errorf("parent job ID is required")
	}
	parentInvocationHash := strings.TrimSpace(req.ParentInvocationHash)
	if !req.AllParentInvocations && parentInvocationHash == "" {
		return jobdb.ListJobsRequest{}, fmt.Errorf("parent invocation hash is required unless all parent invocations are requested")
	}

	metadataFilter, err := jobdb.Metadata().EqualFilter(starter.MetaFieldParentTenantID, parentTenantID)
	if err != nil {
		return jobdb.ListJobsRequest{}, err
	}
	parentJobFilter, err := jobdb.Metadata().EqualFilter(starter.MetaFieldParentJobID, parentJobID)
	if err != nil {
		return jobdb.ListJobsRequest{}, err
	}
	metadataFilter, err = metadataFilter.AndFilter(parentJobFilter)
	if err != nil {
		return jobdb.ListJobsRequest{}, err
	}
	if !req.AllParentInvocations {
		invocationFilter, err := jobdb.Metadata().EqualFilter(starter.MetaFieldParentInvocationHash, parentInvocationHash)
		if err != nil {
			return jobdb.ListJobsRequest{}, err
		}
		metadataFilter, err = metadataFilter.AndFilter(invocationFilter)
		if err != nil {
			return jobdb.ListJobsRequest{}, err
		}
	}

	return jobdb.ListJobsRequest{
		TenantIds:      []string{tenantID},
		Statuses:       append([]jobdb.JobStatus(nil), req.Statuses...),
		Stores:         append([]jobdb.JobStore(nil), req.Stores...),
		JobTypes:       []string{starter.RecipeJobType},
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

func ListChildRecipeJobs(ctx context.Context, lister Lister, req ListChildRecipeJobsRequest) (ListRecipeJobsResponse, error) {
	if lister == nil {
		return ListRecipeJobsResponse{}, fmt.Errorf("job lister is required")
	}
	listReq, err := BuildListChildJobsRequest(req)
	if err != nil {
		return ListRecipeJobsResponse{}, err
	}
	resp, err := lister.ListJobs(ctx, listReq)
	if err != nil {
		return ListRecipeJobsResponse{}, err
	}
	return recipeJobsFromSummaries(resp.Jobs, resp.NextPageToken)
}

func ListChildRecipeJobsFromWorkflow(ctx context.Context, lister workflowLister, req ListChildRecipeJobsRequest) (ListRecipeJobsResponse, error) {
	if lister == nil {
		return ListRecipeJobsResponse{}, fmt.Errorf("job lister is required")
	}
	listReq, err := BuildListChildJobsRequest(req)
	if err != nil {
		return ListRecipeJobsResponse{}, err
	}
	items, nextPageToken, err := lister.ListJobs(ctx, listReq)
	if err != nil {
		return ListRecipeJobsResponse{}, err
	}
	summaries := make([]jobdb.JobSummary, 0, len(items))
	for _, item := range items {
		summaries = append(summaries, item.JobSummary)
	}
	return recipeJobsFromSummaries(summaries, nextPageToken)
}

func CollectStartedJobs(ctx context.Context, lister workflowLister, current jobcontext.Current) (jobcontext.StartedJobsContext, error) {
	if lister == nil || !current.HasJob() || strings.TrimSpace(current.InvocationHash) == "" {
		return jobcontext.StartedJobsContext{}, nil
	}
	req := ListChildRecipeJobsRequest{
		TenantID:             current.TenantID,
		ParentTenantID:       current.TenantID,
		ParentJobID:          current.JobID,
		ParentInvocationHash: current.InvocationHash,
		Stores:               []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived},
	}
	var all []RecipeJob
	for {
		resp, err := ListChildRecipeJobsFromWorkflow(ctx, lister, req)
		if err != nil {
			return jobcontext.StartedJobsContext{}, err
		}
		all = append(all, resp.Jobs...)
		if strings.TrimSpace(resp.NextPageToken) == "" {
			break
		}
		req.PageToken = resp.NextPageToken
	}
	return StartedJobsContextFromRecipeJobs(all), nil
}

func StartedJobsContextFromRecipeJobs(jobs []RecipeJob) jobcontext.StartedJobsContext {
	out := jobcontext.StartedJobsContext{
		JobIDs: make([]string, 0, len(jobs)),
		Items:  make([]jobcontext.StartedJobContext, 0, len(jobs)),
	}
	for _, job := range jobs {
		if strings.TrimSpace(job.JobID) == "" {
			continue
		}
		out.JobIDs = append(out.JobIDs, job.JobID)
		parentInvocationHash := ""
		if job.Parent != nil {
			parentInvocationHash = job.Parent.InvocationHash
		}
		out.Items = append(out.Items, jobcontext.StartedJobContext{
			TenantID:             job.TenantID,
			JobID:                job.JobID,
			RecipeName:           job.RecipeName,
			Status:               string(job.Status),
			ParentInvocationHash: parentInvocationHash,
		})
	}
	return out
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
		if parent := parentFromMetadata(meta); parent.HasJob() {
			job.Parent = &parent
		}
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
		if job.Parent == nil && start.Parent != nil && start.Parent.HasJob() {
			parent := *start.Parent
			job.Parent = &parent
		}
	}
	if normalized, err := compiler.NormalizeGitRepositorySource(job.RepositorySource); err == nil {
		job.RepositorySource = normalized
	}

	return job, true, nil
}

func recipeJobsFromSummaries(summaries []jobdb.JobSummary, nextPageToken string) (ListRecipeJobsResponse, error) {
	jobs := make([]RecipeJob, 0, len(summaries))
	for _, summary := range summaries {
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
		NextPageToken: nextPageToken,
	}, nil
}

func parentFromMetadata(meta *starter.JobMetadata) jobcontext.Parent {
	if meta == nil {
		return jobcontext.Parent{}
	}
	return jobcontext.Parent{
		TenantID:           meta.ParentTenantID,
		JobID:              meta.ParentJobID,
		JobType:            meta.ParentJobType,
		OpType:             meta.ParentOpType,
		OpStep:             meta.ParentOpStep,
		OpTaskType:         meta.ParentOpTaskType,
		CellName:           meta.ParentCellName,
		RepositorySource:   meta.ParentRepositorySource,
		GitRef:             meta.ParentGitRef,
		InvocationPath:     meta.ParentInvocationPath,
		InvocationSequence: meta.ParentInvocationSequence,
		InvocationHash:     meta.ParentInvocationHash,
	}
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
