package model

import (
	"time"

	"github.com/colony-2/swf-go/pkg/swf"
)

type WorkflowStatus string

const (
	WorkflowStatusRunning    WorkflowStatus = "running"
	WorkflowStatusCompleted  WorkflowStatus = "completed"
	WorkflowStatusFailed     WorkflowStatus = "failed"
	WorkflowStatusCanceled   WorkflowStatus = "canceled"
	WorkflowStatusTerminated WorkflowStatus = "terminated"
	WorkflowStatusTimedOut   WorkflowStatus = "timed_out"
	WorkflowStatusUnknown    WorkflowStatus = "unknown"
)

type ChapterStatus string

const (
	ChapterStatusPending   ChapterStatus = "pending"
	ChapterStatusRunning   ChapterStatus = "running"
	ChapterStatusCompleted ChapterStatus = "completed"
	ChapterStatusFailed    ChapterStatus = "failed"
	ChapterStatusSkipped   ChapterStatus = "skipped"
)

type ActorType string

const (
	ActorTypeUser  ActorType = "user"
	ActorTypeAgent ActorType = "agent"
)

type ActorUser struct {
	Email string `json:"email"`
}

type ActorAgent struct {
	Cell           string `json:"cell"`
	WorkflowName   string `json:"workflow_name"`
	ExecutionID    string `json:"execution_id"`
	InvocationHash string `json:"invocation_hash"`
}

type Actor struct {
	Type  ActorType   `json:"type"`
	User  *ActorUser  `json:"user,omitempty"`
	Agent *ActorAgent `json:"agent,omitempty"`
}

type ArtifactReference struct {
	ArtifactID   string    `json:"artifact_id"`
	ArtifactType string    `json:"artifact_type"`
	Name         string    `json:"name"`
	SizeBytes    *int64    `json:"size_bytes,omitempty"`
	URL          *string   `json:"url,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type WorkflowOutcome struct {
	JobID          string                 `json:"job_id"`
	Status         WorkflowStatus         `json:"status"`
	AttemptOrdinal *int64                 `json:"attempt_ordinal,omitempty"`
	Output         map[string]interface{} `json:"output,omitempty"`
	Error          *string                `json:"error,omitempty"`
	Artifacts      []ArtifactReference    `json:"artifacts"`
}

type ChapterDetail struct {
	ChapterNumber int                     `json:"chapter_number"`
	ChapterType   string                  `json:"chapter_type"`
	OpName        *string                 `json:"op_name,omitempty"`
	Status        ChapterStatus           `json:"status"`
	StartTime     *time.Time              `json:"start_time,omitempty"`
	EndTime       *time.Time              `json:"end_time,omitempty"`
	Input         map[string]interface{}  `json:"input"`
	Output        *map[string]interface{} `json:"output,omitempty"`
	Error         *string                 `json:"error,omitempty"`
	Artifacts     []ArtifactReference     `json:"artifacts"`
}

type WorkflowSummary struct {
	WorkflowID  string         `json:"workflow_id"`
	RunID       string         `json:"run_id"`
	Status      WorkflowStatus `json:"status"`
	RecipeName  string         `json:"recipe_name"`
	InputHash   *string        `json:"input_hash,omitempty"`
	SubmittedAt *time.Time     `json:"submitted_at,omitempty"`
	CellID      *string        `json:"cell_id,omitempty"`
	CellName    *string        `json:"cell_name,omitempty"`
	StartTime   *time.Time     `json:"start_time,omitempty"`
	CloseTime   *time.Time     `json:"close_time,omitempty"`
	Actor       Actor          `json:"actor"`
	CreatedAt   time.Time      `json:"created_at"`
}

type WorkflowDetail struct {
	WorkflowID string                  `json:"workflow_id"`
	RunID      string                  `json:"run_id"`
	Status     WorkflowStatus          `json:"status"`
	RecipeName string                  `json:"recipe_name"`
	CellID     *string                 `json:"cell_id,omitempty"`
	CellName   *string                 `json:"cell_name,omitempty"`
	StartTime  *time.Time              `json:"start_time,omitempty"`
	CloseTime  *time.Time              `json:"close_time,omitempty"`
	Actor      Actor                   `json:"actor"`
	GitRef     *string                 `json:"git_ref,omitempty"`
	GitCommit  *string                 `json:"git_commit,omitempty"`
	Chapters   []ChapterDetail         `json:"chapters"`
	RawJobData *map[string]interface{} `json:"raw_job_data,omitempty"`
	CreatedAt  time.Time               `json:"created_at"`
}

type ListWorkflowsRequest struct {
	ProjectID string
	Statuses  []WorkflowStatus
	CellID    *string
	Since     *time.Time
	Until     *time.Time
	Limit     int
	Offset    int
}

type GetWorkflowRequest struct {
	ProjectID         string
	WorkflowID        string
	IncludeRawJobData bool
}

type GetWorkflowOutcomeRequest struct {
	ProjectID string
	JobID     string
}

type GetArtifactByOrdinalRequest struct {
	ProjectID    string
	JobID        string
	TaskOrdinal  int64
	ArtifactName string
}

type GetWorkflowArtifactRequest struct {
	ProjectID     string
	WorkflowID    string
	ChapterNumber int
	ArtifactName  string
}

type ArtifactData struct {
	Content   []byte
	Filename  string
	SizeBytes int64
	Metadata  map[string]string
}

type StartWorkflowRequest struct {
	ProjectID      string
	RecipeName     string
	CellID         string
	Inputs         map[string]interface{}
	GitRef         *string
	ActorEmail     *string
	IdempotencyKey *string
	Prerequisites  []swf.JobPrerequisite
}
