package recipe

import (
	"context"
	"errors"
	"fmt"

	"github.com/colony-2/c2j/pkg/core/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
)

type fakeJobTool struct {
	key       swf.JobKey
	awaitErr  error
	awaitArgs []string
}

func (f *fakeJobTool) GetJobKey() swf.JobKey {
	return f.key
}

func (f *fakeJobTool) AwaitJobs(jobIds ...string) error {
	f.awaitArgs = append(f.awaitArgs, jobIds...)
	return f.awaitErr
}

type fakeWorkflowControl struct {
	startKeys       []swf.JobKey
	startErrs       []error
	startRequests   []workflowctl.StartJob
	startSawTx      []bool
	jobResultFunc   func(ctx context.Context, key swf.JobKey) (swf.JobData, error)
	getArtifactFunc func(ctx context.Context, tenantId string, key swf.ArtifactKey) swf.Artifact
	jobResultCalls  int
}

func (f *fakeWorkflowControl) StartJob(ctx context.Context, req workflowctl.StartJob) (swf.JobKey, error) {
	idx := len(f.startRequests)
	f.startRequests = append(f.startRequests, req)
	_, ok := swf.TxFromCtx(ctx)
	f.startSawTx = append(f.startSawTx, ok)
	if idx < len(f.startErrs) && f.startErrs[idx] != nil {
		return swf.JobKey{}, f.startErrs[idx]
	}
	if idx < len(f.startKeys) {
		return f.startKeys[idx], nil
	}
	if req.JobID != "" {
		return swf.JobKey{TenantId: req.TenantId, JobId: req.JobID}, nil
	}
	return swf.JobKey{TenantId: req.TenantId, JobId: fmt.Sprintf("job-%d", idx)}, nil
}

func (f *fakeWorkflowControl) Cancel(ctx context.Context, jobKey swf.JobKey) error {
	return errors.New("not implemented")
}

func (f *fakeWorkflowControl) ListJobs(ctx context.Context, request swf.ListJobsRequest) ([]workflowctl.JobItem, string, error) {
	return nil, "", errors.New("not implemented")
}

func (f *fakeWorkflowControl) CompleteTask(ctx context.Context, jobKey swf.JobKey, taskOrdinal int64, hash string, data any) error {
	return errors.New("not implemented")
}

func (f *fakeWorkflowControl) GetWaitingTask(ctx context.Context, jobKey swf.JobKey) (workflowctl.TaskHandle, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeWorkflowControl) GetArtifactLazy(ctx context.Context, tenantId string, key swf.ArtifactKey) swf.Artifact {
	if f.getArtifactFunc != nil {
		return f.getArtifactFunc(ctx, tenantId, key)
	}
	return swf.NewArtifactFromBytes(key.Name, []byte("artifact"))
}

func (f *fakeWorkflowControl) JobResult(ctx context.Context, key swf.JobKey) (swf.JobData, error) {
	f.jobResultCalls++
	if f.jobResultFunc == nil {
		return nil, errors.New("job result not configured")
	}
	return f.jobResultFunc(ctx, key)
}

type errorTaskData struct {
	data         swf.Data
	artifacts    []swf.Artifact
	dataErr      error
	artifactsErr error
}

func (e *errorTaskData) GetData() (swf.Data, error) {
	if e.dataErr != nil {
		return nil, e.dataErr
	}
	return e.data, nil
}

func (e *errorTaskData) GetDataOrPanic() swf.Data {
	data, err := e.GetData()
	if err != nil {
		panic(err)
	}
	return data
}

func (e *errorTaskData) GetArtifacts() ([]swf.Artifact, error) {
	if e.artifactsErr != nil {
		return nil, e.artifactsErr
	}
	return e.artifacts, nil
}
