package workflow

import (
	"context"
	"fmt"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// CachedTaskOutput reads a successful historical task without rerunning it.
// Coordinates and input hash must match the runtime's mismatch report exactly.
func (s *SWFWorkflowControl) CachedTaskOutput(ctx context.Context, key jobdb.JobKey, taskType string, ordinal int64, inputHash string) (jobdb.TaskData, error) {
	run, err := s.Engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true, IncludeArtifacts: true})
	if err != nil {
		return nil, err
	}
	if run.Job.JobKey != key {
		return nil, fmt.Errorf("cached task job identity does not match")
	}
	for _, job := range run.Attempts {
		for _, task := range job.Tasks {
			for _, attempt := range task.Attempts {
				if attempt.Ordinal != ordinal {
					continue
				}
				if task.TaskType != taskType || attempt.InputHash != inputHash {
					return nil, fmt.Errorf("cached task identity does not match at ordinal %d", ordinal)
				}
				if attempt.Outcome.Status != jobdb.TaskOutcomeStatusSucceeded || attempt.Outcome.Error != nil || attempt.Output == nil {
					return nil, fmt.Errorf("cached task at ordinal %d has no successful output", ordinal)
				}
				artifacts := make([]jobdb.Artifact, 0, len(attempt.Output.Artifacts))
				for _, info := range attempt.Output.Artifacts {
					artifactKey := jobdb.ArtifactKey{JobId: key.JobId, TaskOrdinal: ordinal, Name: info.Name, SizeBytes: info.SizeBytes}
					if info.Key != nil {
						artifactKey = *info.Key
					}
					if err := artifactKey.Validate(); err != nil {
						return nil, err
					}
					if artifactKey.JobId != key.JobId || artifactKey.TaskOrdinal != ordinal {
						return nil, fmt.Errorf("cached task artifact identity does not match")
					}
					artifacts = append(artifacts, artifactKey.ToLazyArtifact(s.Engine, key.TenantId))
				}
				return &jobdb.SimpleTaskData{Data: append([]byte(nil), attempt.Output.Data...), Artifacts: artifacts}, nil
			}
		}
	}
	return nil, fmt.Errorf("cached task ordinal %d not found", ordinal)
}
