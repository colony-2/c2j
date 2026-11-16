package changestest

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// ReadOnlyRuntime records and rejects every writer/worker/secret entry point.
// Wrap after fixture setup, then assert Mutations is zero as well as comparing
// complete Inventory results. Successful read methods still use the real backend.
type ReadOnlyRuntime struct {
	jobdb.WorkflowRuntime
	Mutations atomic.Int64
}

func (r *ReadOnlyRuntime) reject() error {
	r.Mutations.Add(1)
	return errors.New("Changes attempted a mutation or execution capability")
}
func (r *ReadOnlyRuntime) PutChapter(context.Context, jobdb.PutChapterRequest) error {
	return r.reject()
}
func (r *ReadOnlyRuntime) SubmitJob(context.Context, jobdb.SubmitJobRequest) (jobdb.JobHandle, error) {
	return jobdb.JobHandle{}, r.reject()
}
func (r *ReadOnlyRuntime) SubmitRestartJob(context.Context, jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	return jobdb.JobHandle{}, r.reject()
}
func (r *ReadOnlyRuntime) CancelJob(context.Context, jobdb.CancelJobRequest) error { return r.reject() }
func (r *ReadOnlyRuntime) ClearCrashConcern(context.Context, jobdb.ClearCrashConcernRequest) error {
	return r.reject()
}
func (r *ReadOnlyRuntime) PollWork(context.Context, jobdb.PollWorkRequest) ([]jobdb.ExecutionLease, error) {
	return nil, r.reject()
}
func (r *ReadOnlyRuntime) GetJobLease(context.Context, jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	return nil, r.reject()
}
func (r *ReadOnlyRuntime) CompleteTaskIfWaiting(context.Context, jobdb.CompleteTaskIfWaitingRequest) error {
	return r.reject()
}
func (r *ReadOnlyRuntime) CreateCell(context.Context, jobdb.CreateCellRequest) (jobdb.Cell, error) {
	return jobdb.Cell{}, r.reject()
}
func (r *ReadOnlyRuntime) UpdateCell(context.Context, jobdb.UpdateCellRequest) (jobdb.Cell, error) {
	return jobdb.Cell{}, r.reject()
}
func (r *ReadOnlyRuntime) RetireCell(context.Context, jobdb.RetireCellRequest) (jobdb.Cell, error) {
	return jobdb.Cell{}, r.reject()
}
func (r *ReadOnlyRuntime) AddGoalCandidate(context.Context, jobdb.AddGoalCandidateRequest) (jobdb.GoalCandidateInfo, error) {
	return jobdb.GoalCandidateInfo{}, r.reject()
}
func (r *ReadOnlyRuntime) FailGoal(context.Context, jobdb.FailGoalRequest) (jobdb.GoalInfo, error) {
	return jobdb.GoalInfo{}, r.reject()
}
func (r *ReadOnlyRuntime) ResolveSecrets(context.Context, jobdb.ResolveSecretsRequest) (jobdb.SecretResolveResult, error) {
	return jobdb.SecretResolveResult{}, r.reject()
}
