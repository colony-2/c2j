package swfutil

import (
	"context"

	"github.com/colony-2/swf-go/pkg/swf"
)

func JobStatus(ctx context.Context, engine swf.SWFEngine, key swf.JobKey) (swf.JobStatus, error) {
	job, err := engine.GetJob(ctx, key)
	if err != nil {
		return "", err
	}
	return job.Status, nil
}

func JobResult(ctx context.Context, engine swf.SWFEngine, key swf.JobKey) (swf.JobData, error) {
	run, err := engine.GetJobRun(ctx, swf.GetJobRunRequest{
		JobKey:           key,
		IncludeOutputs:   true,
		IncludeArtifacts: true,
	})
	if err != nil {
		return nil, err
	}
	return run.GetOutput(engine, key.TenantId)
}
