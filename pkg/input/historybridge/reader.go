package historybridge

import (
	"context"
	"errors"
	"io"

	"github.com/colony-2/c2j/pkg/jobinputs"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type reader struct {
	rt        Runtime
	wait      WaitReader
	authority jobinputs.Authority
}

func (r *reader) GetJobRun(ctx context.Context, q jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error) {
	if e := r.authority.Guard(ctx, q.JobKey.JobId); e != nil {
		record(ctx, e)
		return jobdb.GetJobRunResponse{}, e
	}
	v, e := jobdb.GetJobRun(ctx, r.rt, q)
	record(ctx, e)
	return v, e
}
func (r *reader) GetWaitingTask(ctx context.Context, key jobdb.JobKey) (workflowctl.TaskHandle, error) {
	if e := r.authority.Guard(ctx, key.JobId); e != nil {
		record(ctx, e)
		return nil, e
	}
	v, e := r.wait.GetWaitingTask(ctx, key)
	// No current wait is a normal historical observation.
	if e != nil && !errors.Is(e, jobdb.ErrJobNotFound) {
		record(ctx, e)
	}
	return v, e
}
func (r *reader) GetArtifactLazy(ctx context.Context, tenant string, key jobdb.ArtifactKey) jobdb.Artifact {
	return jobdb.NewArtifact(key.Name, func() (io.ReadCloser, int64, error) {
		stream, e := r.open(ctx, tenant, key)
		record(ctx, e)
		return stream, key.SizeBytes, e
	}, nil)
}
func (r *reader) open(ctx context.Context, tenant string, key jobdb.ArtifactKey) (io.ReadCloser, error) {
	if tenant != r.authority.Tenant {
		return nil, jobdb.ErrLeasePermissionDenied
	}
	if e := r.authority.Guard(ctx, key.JobId); e != nil {
		return nil, e
	}
	if key.SizeBytes < 0 {
		return nil, jobinputs.Diagnostic("size_mismatch", "negative retained size", nil)
	}
	// This is the exact original producer, never the selected job's inventory.
	if _, e := r.rt.GetJob(ctx, jobdb.JobKey{TenantId: tenant, JobId: key.JobId}); e != nil {
		return nil, e
	}
	a, e := r.rt.OpenArtifact(ctx, jobdb.ArtifactRef{JobKey: jobdb.JobKey{TenantId: tenant, JobId: key.JobId}, Ordinal: key.TaskOrdinal, Name: key.Name})
	if e != nil {
		return nil, e
	}
	if a == nil {
		return nil, jobinputs.Diagnostic("unavailable", "retained artifact descriptor missing", nil)
	}
	if a.Name() != key.Name || (a.Size() >= 0 && a.Size() != key.SizeBytes) {
		return nil, jobinputs.Diagnostic("size_mismatch", "runtime descriptor differs from retained member", nil)
	}
	source, e := a.Open()
	if e != nil {
		return nil, e
	}
	if source == nil {
		return nil, jobinputs.Diagnostic("read", "nil retained artifact stream", nil)
	}
	verified := jobinputs.VerifiedStream(ctx, source, key.SizeBytes, func() error { return r.authority.Guard(ctx, key.JobId) })
	return &recorded{ReadCloser: verified, ctx: ctx}, nil
}

type recorded struct {
	io.ReadCloser
	ctx context.Context
}

func (r *recorded) Read(p []byte) (int, error) {
	n, e := r.ReadCloser.Read(p)
	record(r.ctx, e)
	return n, e
}
func (r *recorded) Close() error { e := r.ReadCloser.Close(); record(r.ctx, e); return e }
