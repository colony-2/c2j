// Package localruntime installs trusted local-administrator cell authority for
// embedded development runtimes. Remote clients never use this wrapper.
package localruntime

import (
	"context"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

func Context(ctx context.Context) context.Context {
	return jobdb.WithCellAuthority(ctx, jobdb.CellAuthority{Principal: "c2j-local-admin", TenantID: "*", Permissions: map[string]bool{
		"cells.create": true, "cells.get": true, "cells.list": true, "cells.update": true, "cells.retire": true, "jobs.submit": true, "jobs.list": true, "jobs.acquire": true,
	}})
}

type Runtime struct {
	jobdb.WorkflowRuntime
	jobdb.JobSchemaRegistry
	jobdb.JobDeletionRuntime
}

func New(rt jobdb.WorkflowRuntime) Runtime {
	schema, _ := rt.(jobdb.JobSchemaRegistry)
	deletion, _ := rt.(jobdb.JobDeletionRuntime)
	return Runtime{WorkflowRuntime: rt, JobSchemaRegistry: schema, JobDeletionRuntime: deletion}
}

func (r Runtime) CreateCell(ctx context.Context, req jobdb.CreateCellRequest) (jobdb.Cell, error) {
	return r.WorkflowRuntime.CreateCell(Context(ctx), req)
}
func (r Runtime) GetCell(ctx context.Context, req jobdb.CellKey) (jobdb.Cell, error) {
	return r.WorkflowRuntime.GetCell(Context(ctx), req)
}
func (r Runtime) ListCells(ctx context.Context, req jobdb.ListCellsRequest) (jobdb.ListCellsResponse, error) {
	return r.WorkflowRuntime.ListCells(Context(ctx), req)
}
func (r Runtime) UpdateCell(ctx context.Context, req jobdb.UpdateCellRequest) (jobdb.Cell, error) {
	return r.WorkflowRuntime.UpdateCell(Context(ctx), req)
}
func (r Runtime) RetireCell(ctx context.Context, req jobdb.RetireCellRequest) (jobdb.Cell, error) {
	return r.WorkflowRuntime.RetireCell(Context(ctx), req)
}
func (r Runtime) SubmitJob(ctx context.Context, req jobdb.SubmitJobRequest) (jobdb.JobHandle, error) {
	return r.WorkflowRuntime.SubmitJob(Context(ctx), req)
}
func (r Runtime) SubmitRestartJob(ctx context.Context, req jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	return r.WorkflowRuntime.SubmitRestartJob(Context(ctx), req)
}
func (r Runtime) ListJobs(ctx context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	return r.WorkflowRuntime.ListJobs(Context(ctx), req)
}
func (r Runtime) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	return r.WorkflowRuntime.GetJobLease(Context(ctx), req)
}
func (r Runtime) UpsertSchedule(ctx context.Context, req jobdb.UpsertScheduleRequest) (jobdb.ScheduleInfo, error) {
	return r.WorkflowRuntime.UpsertSchedule(Context(ctx), req)
}
func (r Runtime) ResumeSchedule(ctx context.Context, req jobdb.ScheduleMutationRequest) (jobdb.ScheduleInfo, error) {
	return r.WorkflowRuntime.ResumeSchedule(Context(ctx), req)
}
func (r Runtime) TriggerSchedule(ctx context.Context, req jobdb.TriggerScheduleRequest) (jobdb.JobHandle, error) {
	return r.WorkflowRuntime.TriggerSchedule(Context(ctx), req)
}
func (r Runtime) PollWork(ctx context.Context, req jobdb.PollWorkRequest) ([]jobdb.ExecutionLease, error) {
	return r.WorkflowRuntime.PollWork(Context(ctx), req)
}
