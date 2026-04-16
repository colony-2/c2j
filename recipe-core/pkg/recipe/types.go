package recipe

import (
	"time"
)

// Recipe represents a discovered recipe with its metadata
type RecipeFile struct {
	// Core identity
	ID          string
	Version     string
	Description string

	// File paths
	BasePath string // Directory containing the recipe

	// Content
	Recipe Recipe

	// Metadata
	Hash         string // Canonical hash of recipe content
	LastModified time.Time
}

// WorkerStatus represents the status of a recipe's worker
type WorkerStatus string

const (
	WorkerStatusRunning  WorkerStatus = "running"
	WorkerStatusStopped  WorkerStatus = "stopped"
	WorkerStatusFailed   WorkerStatus = "failed"
	WorkerStatusStarting WorkerStatus = "starting"
)

// Job represents an execution instance of a recipe
type Job struct {
	ID            string
	RecipeName    string
	RecipeVersion string
	Status        JobStatus
	StartTime     time.Time
	EndTime       *time.Time
	UpdateTime    time.Time
	Input         map[string]interface{}
	Output        map[string]interface{}
	Error         string

	// Optional fields
	Duration     *time.Duration
	Inputs       map[string]interface{} // alias for Input
	Outputs      map[string]interface{} // alias for Output
	Activities   []*ActivityExecution
	WorkflowType string

	ExecutionInfo *WorkflowExecutionInfo
}

// WorkflowExecutionInfo contains job info
type WorkflowExecutionInfo struct {
	JobId string
}

// JobStatus represents the execution status of a job
type JobStatus string

const (
	JobStatusUnknown    JobStatus = "unknown"
	JobStatusRunning    JobStatus = "running"
	JobStatusCompleted  JobStatus = "completed"
	JobStatusFailed     JobStatus = "failed"
	JobStatusCanceled   JobStatus = "canceled"
	JobStatusTerminated JobStatus = "terminated"
)

// ActivityExecution represents the execution state of an activity within a job
type ActivityExecution struct {
	Name      string // Activity name from YAML definition
	Status    string // Use string for flexibility
	StartTime time.Time
	EndTime   time.Time
	Duration  *time.Duration
	Result    map[string]interface{}
	Error     string
	Attempt   int

	ActivityID   string
	ActivityType string
}

// ActivityStatus represents the execution status of an activity
type ActivityStatus string

const (
	ActivityStatusPending   ActivityStatus = "pending"
	ActivityStatusRunning   ActivityStatus = "running"
	ActivityStatusCompleted ActivityStatus = "completed"
	ActivityStatusFailed    ActivityStatus = "failed"
	ActivityStatusSkipped   ActivityStatus = "skipped"
)

// RecipeManifest represents the recipe.yaml structure
type RecipeManifest struct {
	Recipe struct {
		Name        string `yaml:"name"`
		Version     string `yaml:"version"`
		Description string `yaml:"description"`
		Files       struct {
			Workflow   string `yaml:"workflow"`
			Activities string `yaml:"activities"`
			Agents     string `yaml:"agents"`
		} `yaml:"files"`
	} `yaml:"recipe"`
}

// JobFilter represents filter criteria for listing jobs
type JobFilter struct {
	RecipeName string
	Status     *JobStatus
	StartTime  *time.Time
	EndTime    *time.Time
	Limit      int
}
