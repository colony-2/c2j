package recipe

import (
	"context"
	"errors"
	"fmt"

	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type fakeJobTool struct {
	key       jobdb.JobKey
	awaitErr  error
	awaitArgs []string
}

func (f *fakeJobTool) GetJobKey() jobdb.JobKey {
	return f.key
}

func (f *fakeJobTool) AwaitJobs(jobIds ...string) error {
	f.awaitArgs = append(f.awaitArgs, jobIds...)
	return f.awaitErr
}

type fakeWorkflowControl struct {
	startKeys        []jobdb.JobKey
	startErrs        []error
	startRequests    []workflowctl.StartJob
	startSawTx       []bool
	jobResultFunc    func(ctx context.Context, key jobdb.JobKey) (jobdb.JobData, error)
	inspectFunc      func(ctx context.Context, key jobdb.JobKey) (workflowctl.JobInspection, error)
	getArtifactFunc  func(ctx context.Context, tenantId string, key jobdb.ArtifactKey) jobdb.Artifact
	getArtifactCalls int
	jobResultCalls   int
	inspectCalls     int
}

func (f *fakeWorkflowControl) StartJob(ctx context.Context, req workflowctl.StartJob) (jobdb.JobKey, error) {
	idx := len(f.startRequests)
	f.startRequests = append(f.startRequests, req)
	_, ok := jobdb.TxFromCtx(ctx)
	f.startSawTx = append(f.startSawTx, ok)
	if idx < len(f.startErrs) && f.startErrs[idx] != nil {
		return jobdb.JobKey{}, f.startErrs[idx]
	}
	if idx < len(f.startKeys) {
		return f.startKeys[idx], nil
	}
	if req.JobID != "" {
		return jobdb.JobKey{TenantId: req.TenantId, JobId: req.JobID}, nil
	}
	return jobdb.JobKey{TenantId: req.TenantId, JobId: fmt.Sprintf("job-%d", idx)}, nil
}

func (f *fakeWorkflowControl) Cancel(ctx context.Context, jobKey jobdb.JobKey) error {
	return errors.New("not implemented")
}

func (f *fakeWorkflowControl) ListJobs(ctx context.Context, request jobdb.ListJobsRequest) ([]workflowctl.JobItem, string, error) {
	return nil, "", errors.New("not implemented")
}

func (f *fakeWorkflowControl) CompleteTask(ctx context.Context, jobKey jobdb.JobKey, taskOrdinal int64, hash string, data any) error {
	return errors.New("not implemented")
}

func (f *fakeWorkflowControl) GetWaitingTask(ctx context.Context, jobKey jobdb.JobKey) (workflowctl.TaskHandle, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeWorkflowControl) GetArtifactLazy(ctx context.Context, tenantId string, key jobdb.ArtifactKey) jobdb.Artifact {
	f.getArtifactCalls++
	if f.getArtifactFunc != nil {
		return f.getArtifactFunc(ctx, tenantId, key)
	}
	return jobdb.NewArtifactFromBytes(key.Name, []byte("artifact"))
}

func (f *fakeWorkflowControl) JobResult(ctx context.Context, key jobdb.JobKey) (jobdb.JobData, error) {
	f.jobResultCalls++
	if f.jobResultFunc == nil {
		return nil, errors.New("job result not configured")
	}
	return f.jobResultFunc(ctx, key)
}

func (f *fakeWorkflowControl) InspectJob(ctx context.Context, key jobdb.JobKey) (workflowctl.JobInspection, error) {
	f.inspectCalls++
	if f.inspectFunc == nil {
		return workflowctl.JobInspection{}, errors.New("inspect job not configured")
	}
	return f.inspectFunc(ctx, key)
}

type errorTaskData struct {
	data         jobdb.Data
	artifacts    []jobdb.Artifact
	dataErr      error
	artifactsErr error
}

func (e *errorTaskData) GetData() (jobdb.Data, error) {
	if e.dataErr != nil {
		return nil, e.dataErr
	}
	return e.data, nil
}

func (e *errorTaskData) GetDataOrPanic() jobdb.Data {
	data, err := e.GetData()
	if err != nil {
		panic(err)
	}
	return data
}

func (e *errorTaskData) GetArtifacts() ([]jobdb.Artifact, error) {
	if e.artifactsErr != nil {
		return nil, e.artifactsErr
	}
	return e.artifacts, nil
}
