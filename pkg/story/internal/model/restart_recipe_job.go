package model

import coretask "github.com/colony-2/c2j/pkg/task"

// RestartRecipeJobRequest requests a restart of an existing recipe job at a particular SWF step offset.
//
// StepOffset is the next SWF chapter ordinal to execute (0-based). See recipe-core starter.RestartRecipeJob.
// Patch is optional; when present it is applied before resuming execution.
type RestartRecipeJobRequest struct {
	ProjectID  string
	JobID      string
	StepOffset int64
	Patch      *coretask.ContextPatch
}

type RestartRecipeJobResponse struct {
	JobID string `json:"job_id"`
}
