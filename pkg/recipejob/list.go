package recipejob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/joblist"
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
	// IncludeHidden includes mark-deleted jobs within the other filters.
	IncludeHidden    bool
	GoalIDs          []string
	ExecutionFilter  *execution.Filter
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
	// IncludeHidden includes mark-deleted jobs within the other filters.
	IncludeHidden        bool
	ExecutionFilter      *execution.Filter
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
	BlockingWaitSummary *jobdb.DependencyWaitSummary `json:"blocking_wait_summary,omitempty"`
	Metadata            json.RawMessage              `json:"metadata,omitempty"`
	SchemaHash          string                       `json:"schema_hash,omitempty"`
	// Deprecated: not loaded in lists. Use explicit job inspection.
	BlockingWait *jobdb.DependencyWaitInfo `json:"blocking_wait,omitempty"`
	GoalID       string                    `json:"goal_id"`
	// Deprecated: not loaded in lists. Use explicit job inspection.
	OtherGoalIDs []string `json:"other_goal_ids"`
	// Completion fields are persisted terminal results, separate from scheduler Status.
	// Empty status means unavailable. Unknown statuses are preserved verbatim.
	CompletionStatus string `json:"completion_status,omitempty"`
	// Preserved from the list response without additional reads.
	CompletionDetail string         `json:"completion_detail,omitempty"`
	Execution        execution.View `json:"execution"`
	// Preserved from the list response without additional reads.
	ClientPayload         json.RawMessage    `json:"client_payload,omitempty"`
	ClientPayloadRevision int64              `json:"client_payload_revision"`
	TenantID              string             `json:"tenant_id"`
	JobID                 string             `json:"job_id"`
	Status                jobdb.JobStatus    `json:"status"`
	Store                 jobdb.JobStore     `json:"store"`
	JobType               string             `json:"job_type"`
	RecipeName            string             `json:"recipe"`
	Prompt                string             `json:"prompt,omitempty"`
	Title                 string             `json:"title,omitempty"`
	RepositorySource      string             `json:"repo,omitempty"`
	CellID                string             `json:"cell_id,omitempty"`
	CellName              string             `json:"cell_name,omitempty"`
	GitRef                string             `json:"git_ref,omitempty"`
	InputHash             string             `json:"input_hash,omitempty"`
	Parent                *jobcontext.Parent `json:"parent,omitempty"`
	SubmittedAt           *time.Time         `json:"submitted_at,omitempty"`
	CreatedAt             time.Time          `json:"created_at"`
	AvailableAt           time.Time          `json:"available_at"`
	ArchivedAt            *time.Time         `json:"archived_at,omitempty"`
	LeaseExpiresAt        *time.Time         `json:"lease_expires_at,omitempty"`
	ExpiresAt             *time.Time         `json:"expires_at,omitempty"`
	NextRoute             *jobdb.Route       `json:"next_route,omitempty"`
	// Preserved from the list response without additional reads.
	TaskWait *jobdb.TaskWait `json:"task_wait,omitempty"`
	// Preserved from the list response without additional reads.
	WaitFor         []string `json:"wait_for,omitempty"`
	CancelRequested bool     `json:"cancel_requested,omitempty"`
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
		IncludeHidden:  req.IncludeHidden,
		TenantIds:      []string{tenantID},
		Statuses:       append([]jobdb.JobStatus(nil), req.Statuses...),
		Stores:         append([]jobdb.JobStore(nil), req.Stores...),
		JobTypes:       []string{starter.RecipeJobType},
		JobTasks:       append([]jobdb.JobTaskFilter(nil), req.JobTasks...),
		JobKeys:        jobKeys,
		GoalIDs:        append([]string(nil), req.GoalIDs...),
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
	if parentTenantID != tenantID {
		return jobdb.ListJobsRequest{}, fmt.Errorf("child job listing is limited to the selected tenant %q; parent tenant is %q", tenantID, parentTenantID)
	}
	parentJobID := strings.TrimSpace(req.ParentJobID)
	if parentJobID == "" {
		return jobdb.ListJobsRequest{}, fmt.Errorf("parent job ID is required")
	}
	parentInvocationHash := strings.TrimSpace(req.ParentInvocationHash)
	if !req.AllParentInvocations && parentInvocationHash == "" {
		return jobdb.ListJobsRequest{}, fmt.Errorf("parent invocation hash is required unless all parent invocations are requested")
	}

	var metadataFilter jobdb.MetadataFilter
	if !req.AllParentInvocations {
		var err error
		invocationFilter, err := jobdb.Metadata().EqualFilter(starter.MetaFieldParentInvocationHash, parentInvocationHash)
		if err != nil {
			return jobdb.ListJobsRequest{}, err
		}
		metadataFilter = invocationFilter
	}

	return jobdb.ListJobsRequest{
		IncludeHidden:  req.IncludeHidden,
		TenantIds:      []string{tenantID},
		Statuses:       append([]jobdb.JobStatus(nil), req.Statuses...),
		Stores:         append([]jobdb.JobStore(nil), req.Stores...),
		JobTypes:       []string{starter.RecipeJobType},
		ParentJobIDs:   []string{parentJobID},
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
	resp, err := ListExecutionJobs(ctx, lister, listReq, req.ExecutionFilter)
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
	resp, err := ListExecutionJobs(ctx, lister, listReq, req.ExecutionFilter)
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
	resp, err := ListExecutionJobs(ctx, workflowListAdapter{lister}, listReq, req.ExecutionFilter)
	if err != nil {
		return ListRecipeJobsResponse{}, err
	}
	return recipeJobsFromSummaries(resp.Jobs, resp.NextPageToken)
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
		out.GoalIDs = append(out.GoalIDs, job.GoalID)
		parentInvocationHash := ""
		if job.Parent != nil {
			parentInvocationHash = job.Parent.InvocationHash
		}
		out.Items = append(out.Items, jobcontext.StartedJobContext{
			TenantID:             job.TenantID,
			JobID:                job.JobID,
			GoalID:               job.GoalID,
			RecipeName:           job.RecipeName,
			Status:               string(job.Status),
			ParentInvocationHash: parentInvocationHash,
		})
	}
	return out
}

// GetRecipeJob explicitly inspects one selected job. It uses one compact list
// query for metadata/timestamps and one GetJob for details; never call it per
// row to enrich an ordinary list. The lister must also implement GetJob.
func GetRecipeJob(ctx context.Context, lister Lister, req GetRecipeJobRequest) (RecipeJob, error) {
	reader, ok := lister.(interface {
		GetJob(context.Context, jobdb.JobKey) (jobdb.JobInfo, error)
	})
	if !ok {
		return RecipeJob{}, fmt.Errorf("job detail reader is required")
	}
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
	job := resp.Jobs[0]
	info, err := reader.GetJob(ctx, jobdb.JobKey{TenantId: job.TenantID, JobId: job.JobID})
	if errors.Is(err, jobdb.ErrJobNotFound) {
		return RecipeJob{}, ErrJobNotFound
	}
	if err != nil {
		return RecipeJob{}, err
	}
	job.Status = info.Status
	job.GoalID = info.GoalID
	job.OtherGoalIDs = append([]string(nil), info.OtherGoalIDs...)
	job.BlockingWait = info.BlockingWait
	job.BlockingWaitSummary = nil
	if info.BlockingWait != nil {
		job.BlockingWaitSummary = joblist.CloneBlockingWaitSummary(&jobdb.DependencyWaitSummary{WaitID: info.BlockingWait.WaitID, Deadline: info.BlockingWait.Deadline})
	}
	job.CompletionStatus = info.CompletionStatus
	job.CompletionDetail = info.CompletionDetail
	job.WaitFor = append([]string(nil), info.WaitFor...)
	job.NextRoute = jobdb.CloneRoute(info.NextRoute)
	job.TaskWait = jobdb.CloneExecutionState(info.ExecutionState).TaskWait
	job.ClientPayload = append(json.RawMessage(nil), info.ClientPayload...)
	job.ClientPayloadRevision = info.ClientPayloadRevision
	job.SchemaHash = info.SchemaHash
	job.Execution = joblist.ExecutionViewFromInfo(job.Metadata, info)
	return job, nil
}

func RecipeJobFromSummary(summary jobdb.JobSummary) (RecipeJob, bool, error) {
	if strings.TrimSpace(summary.JobType) != "" && summary.JobType != starter.RecipeJobType {
		return RecipeJob{}, false, nil
	}

	meta, err := JobMetadataFromRaw(summary.Metadata)
	if err != nil {
		return RecipeJob{}, false, err
	}

	job := RecipeJob{
		BlockingWaitSummary:   joblist.CloneBlockingWaitSummary(summary.BlockingWaitSummary),
		Metadata:              append(json.RawMessage(nil), summary.Metadata...),
		SchemaHash:            summary.SchemaHash,
		GoalID:                summary.GoalID,
		CompletionStatus:      summary.CompletionStatus,
		CompletionDetail:      summary.CompletionDetail,
		ClientPayload:         append(json.RawMessage(nil), summary.ClientPayload...),
		TaskWait:              jobdb.CloneExecutionState(summary.ExecutionState).TaskWait,
		WaitFor:               append([]string(nil), summary.WaitFor...),
		Execution:             ExecutionView(summary),
		ClientPayloadRevision: summary.ClientPayloadRevision,
		TenantID:              summary.JobKey.TenantId,
		JobID:                 summary.JobKey.JobId,
		Status:                summary.Status,
		Store:                 StoreForJob(summary),
		JobType:               summary.JobType,
		CreatedAt:             summary.CreatedAt,
		AvailableAt:           summary.AvailableAt,
		ArchivedAt:            summary.ArchivedAt,
		LeaseExpiresAt:        summary.LeaseExpiresAt,
		ExpiresAt:             summary.ExpiresAt,
		NextRoute:             jobdb.CloneRoute(summary.NextRoute),
		CancelRequested:       summary.CancelRequested,
	}
	if job.JobType == "" {
		job.JobType = starter.RecipeJobType
	}

	if meta != nil {
		job.InputHash = meta.InputHash
		job.SubmittedAt = meta.SubmittedAt
		job.RecipeName = meta.RecipeName
		job.Prompt = meta.Prompt
		job.Title = meta.Title
		job.CellID = meta.CellID
		job.CellName = meta.CellName
		job.RepositorySource = meta.RepositorySource
		job.GitRef = meta.GitRef
		if parent := parentFromMetadata(meta); parent.HasJob() {
			job.Parent = &parent
		}
	}
	if job.Parent == nil && strings.TrimSpace(summary.ParentJobID) != "" {
		job.Parent = &jobcontext.Parent{
			TenantID: strings.TrimSpace(summary.JobKey.TenantId),
			JobID:    strings.TrimSpace(summary.ParentJobID),
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
		GoalID:             meta.ParentGoalID,
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

func DefaultVisibleStatuses() []jobdb.JobStatus { return joblist.DefaultVisibleStatuses() }

func StoresForStatuses(statuses []jobdb.JobStatus) []jobdb.JobStore {
	return joblist.StoresForStatuses(statuses)
}

func StoreForJob(job jobdb.JobSummary) jobdb.JobStore { return joblist.StoreForJob(job) }
