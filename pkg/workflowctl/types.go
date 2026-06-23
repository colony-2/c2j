package workflowctl

import (
	"context"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type WorkflowControl interface {
	StartJob(ctx context.Context, req StartJob) (jobdb.JobKey, error)
	Cancel(ctx context.Context, jobKey jobdb.JobKey) error
	ListJobs(ctx context.Context, request jobdb.ListJobsRequest) (jobs []JobItem, nextPage string, err error)
	CompleteTask(ctx context.Context, jobKey jobdb.JobKey, taskOrdinal int64, hash string, data any) error
	GetWaitingTask(ctx context.Context, jobKey jobdb.JobKey) (TaskHandle, error)
	GetArtifactLazy(ctx context.Context, tenantId string, key jobdb.ArtifactKey) jobdb.Artifact
	JobResult(ctx context.Context, key jobdb.JobKey) (jobdb.JobData, error)
}

type JobInspector interface {
	InspectJob(ctx context.Context, key jobdb.JobKey) (JobInspection, error)
}

type JobInspection struct {
	JobKey         jobdb.JobKey
	Terminal       bool
	Status         string
	FailureKind    string
	FailureMessage string
	Output         jobdb.JobData
	StartedAt      time.Time
	FinishedAt     time.Time
}

type StartJob struct {
	TenantId     string                 `json:"tenantId"`
	JobID        string                 `json:"job_id,omitempty"`
	RecipeName   string                 `json:"recipe"`
	Inputs       map[string]interface{} `json:"inputs,omitempty"`
	Artifacts    []jobdb.Artifact       `json:"-"`
	ArtifactRefs []recipeartifacts.Ref  `json:"artifact_refs,omitempty"`
	JobContext   contextual.JobContext  `json:"context,omitempty"`
	GitRef       string                 `json:"git,omitempty"`
	SubmittedAt  *time.Time             `json:"submitted_at,omitempty"`
	InputHash    string                 `json:"input_hash,omitempty"`
}

type JobItem struct {
	TaskData jobdb.TaskData
	jobdb.JobSummary
}

type TaskHandle = jobworkflow.TaskHandle
