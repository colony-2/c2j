package compiler

import (
	"context"
	"fmt"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

func unsupportedTestSubmitJob(context.Context, jobdb.SubmitJob) (jobdb.JobKey, error) {
	return jobdb.JobKey{}, fmt.Errorf("submitting jobs is not supported by this test context")
}

func unsupportedTestSubmitRestartJob(context.Context, jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	return jobdb.JobKey{}, fmt.Errorf("submitting restart jobs is not supported by this test context")
}

func (c *countingJobContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitJob(ctx, submit)
}

func (c *countingJobContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitRestartJob(ctx, restart)
}

func (c *catchScriptedJobContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitJob(ctx, submit)
}

func (c *catchScriptedJobContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitRestartJob(ctx, restart)
}

func (p *patchingStubJobContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitJob(ctx, submit)
}

func (p *patchingStubJobContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitRestartJob(ctx, restart)
}

func (s *stubJobContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitJob(ctx, submit)
}

func (s *stubJobContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitRestartJob(ctx, restart)
}

func (c *policyCaptureJobContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitJob(ctx, submit)
}

func (c *policyCaptureJobContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitRestartJob(ctx, restart)
}

func (c *capturingInvocationJobContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitJob(ctx, submit)
}

func (c *capturingInvocationJobContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitRestartJob(ctx, restart)
}

func (c *renameAfterWithinResolutionContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitJob(ctx, submit)
}

func (c *renameAfterWithinResolutionContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	return unsupportedTestSubmitRestartJob(ctx, restart)
}
