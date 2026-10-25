package compiler

import (
	"context"
	"fmt"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

func unsupportedTestSubmitJob(context.Context, jobdb.SubmitJob) (jobdb.JobHandle, error) {
	return jobdb.JobHandle{}, fmt.Errorf("submitting jobs is not supported by this test context")
}

func unsupportedTestSubmitRestartJob(context.Context, jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	return jobdb.JobHandle{}, fmt.Errorf("submitting restart jobs is not supported by this test context")
}

func (c *countingJobContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobHandle, error) {
	return unsupportedTestSubmitJob(ctx, submit)
}

func (c *countingJobContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	return unsupportedTestSubmitRestartJob(ctx, restart)
}

func (c *catchScriptedJobContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobHandle, error) {
	return unsupportedTestSubmitJob(ctx, submit)
}

func (c *catchScriptedJobContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	return unsupportedTestSubmitRestartJob(ctx, restart)
}

func (s *stubJobContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobHandle, error) {
	return unsupportedTestSubmitJob(ctx, submit)
}

func (s *stubJobContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	return unsupportedTestSubmitRestartJob(ctx, restart)
}

func (c *policyCaptureJobContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobHandle, error) {
	return unsupportedTestSubmitJob(ctx, submit)
}

func (c *policyCaptureJobContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	return unsupportedTestSubmitRestartJob(ctx, restart)
}

func (c *capturingInvocationJobContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobHandle, error) {
	return unsupportedTestSubmitJob(ctx, submit)
}

func (c *capturingInvocationJobContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	return unsupportedTestSubmitRestartJob(ctx, restart)
}

func (c *renameAfterWithinResolutionContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobHandle, error) {
	return unsupportedTestSubmitJob(ctx, submit)
}

func (c *renameAfterWithinResolutionContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	return unsupportedTestSubmitRestartJob(ctx, restart)
}

func (*countingJobContext) AwaitGoals(...string) ([]jobdb.DependencyResolution, error) {
	return nil, fmt.Errorf("unexpected AwaitGoals in test context")
}

func (*countingJobContext) AwaitDependencies(...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
	return nil, fmt.Errorf("unexpected AwaitDependencies in test context")
}

func (*catchScriptedJobContext) AwaitGoals(...string) ([]jobdb.DependencyResolution, error) {
	return nil, fmt.Errorf("unexpected AwaitGoals in test context")
}

func (*catchScriptedJobContext) AwaitDependencies(...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
	return nil, fmt.Errorf("unexpected AwaitDependencies in test context")
}

func (*stubJobContext) AwaitGoals(...string) ([]jobdb.DependencyResolution, error) {
	return nil, fmt.Errorf("unexpected AwaitGoals in test context")
}

func (*stubJobContext) AwaitDependencies(...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
	return nil, fmt.Errorf("unexpected AwaitDependencies in test context")
}

func (*policyCaptureJobContext) AwaitGoals(...string) ([]jobdb.DependencyResolution, error) {
	return nil, fmt.Errorf("unexpected AwaitGoals in test context")
}

func (*policyCaptureJobContext) AwaitDependencies(...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
	return nil, fmt.Errorf("unexpected AwaitDependencies in test context")
}

func (*capturingInvocationJobContext) AwaitGoals(...string) ([]jobdb.DependencyResolution, error) {
	return nil, fmt.Errorf("unexpected AwaitGoals in test context")
}

func (*capturingInvocationJobContext) AwaitDependencies(...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
	return nil, fmt.Errorf("unexpected AwaitDependencies in test context")
}

func (*renameAfterWithinResolutionContext) AwaitGoals(...string) ([]jobdb.DependencyResolution, error) {
	return nil, fmt.Errorf("unexpected AwaitGoals in test context")
}

func (*renameAfterWithinResolutionContext) AwaitDependencies(...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
	return nil, fmt.Errorf("unexpected AwaitDependencies in test context")
}
