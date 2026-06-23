package swfutil

import (
	"context"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func JobStatus(ctx context.Context, engine jobworkflow.Engine, key jobdb.JobKey) (jobdb.JobStatus, error) {
	job, err := engine.GetJob(ctx, key)
	if err != nil {
		return "", err
	}
	return job.Status, nil
}

func JobResult(ctx context.Context, engine jobworkflow.Engine, key jobdb.JobKey) (jobdb.JobData, error) {
	run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{
		JobKey:           key,
		IncludeOutputs:   true,
		IncludeArtifacts: true,
	})
	if err != nil {
		return nil, err
	}
	return run.GetOutput(engine, key.TenantId)
}
