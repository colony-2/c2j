package workflowctl

import (
	"context"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type WorkflowControl interface {
	StartJob(ctx context.Context, req StartJob) (jobref.Ref, error)
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
	BlockingWait   *jobdb.DependencyWaitInfo
	GoalID         string
	OtherGoalIDs   []string
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
	Dependencies []jobdb.Dependency     `json:"dependencies,omitempty"`
	Execution    *execution.Submission  `json:"execution,omitempty"`
	TenantId     string                 `json:"tenantId"`
	JobID        string                 `json:"job_id,omitempty"`
	RecipeName   string                 `json:"recipe"`
	Inputs       map[string]interface{} `json:"inputs,omitempty"`
	Artifacts    []jobdb.Artifact       `json:"-"`
	ArtifactRefs []recipeartifacts.Ref  `json:"artifact_refs,omitempty"`
	JobContext   contextual.JobContext  `json:"context,omitempty"`
	Parent       *jobcontext.Parent     `json:"parent,omitempty"`
	GitRef       string                 `json:"git,omitempty"`
	SubmittedAt  *time.Time             `json:"submitted_at,omitempty"`
	InputHash    string                 `json:"input_hash,omitempty"`
}

type JobItem struct {
	// Deprecated: compact listings leave TaskData nil. Use GetWaitingTask explicitly.
	TaskData jobdb.TaskData
	jobdb.JobSummary
}

type TaskHandle = jobworkflow.TaskHandle
