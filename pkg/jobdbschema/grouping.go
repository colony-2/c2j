package jobdbschema

import (
	"context"
	"errors"
	"fmt"

	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type AppendSubmitter interface {
	StartOrAppendJob(context.Context, jobdb.StartOrAppendJob) (jobdb.JobHandle, error)
}

// Append retries only the definitive schema-missing case. A transport error
// may follow acceptance and must never cause an automatic second contribution.
func Append(ctx context.Context, submit AppendSubmitter, registry jobdb.JobSchemaRegistry, req jobdb.StartOrAppendJob) (jobdb.JobHandle, error) {
	if req.Job.Schema == nil {
		if err := SetSubmitJobSchema(&req.Job); err != nil {
			return jobdb.JobHandle{}, err
		}
	}
	if err := executioncatalog.CheckSubmission(ctx, req.Job); err != nil {
		return jobdb.JobHandle{}, err
	}
	h, err := submit.StartOrAppendJob(ctx, req)
	if !errors.Is(err, jobdb.ErrJobSchemaNotFound) {
		return h, err
	}
	if err := Register(ctx, registry, req.Job.TenantId); err != nil {
		return jobdb.JobHandle{}, fmt.Errorf("register grouped recipe schema: %w", err)
	}
	if err := executioncatalog.CheckSubmission(ctx, req.Job); err != nil {
		return jobdb.JobHandle{}, err
	}
	return submit.StartOrAppendJob(ctx, req)
}

func (e WorkflowEngine) StartOrAppendJob(ctx context.Context, req jobdb.StartOrAppendJob) (jobdb.JobHandle, error) {
	if req.Job.JobType != "recipe" {
		if err := executioncatalog.CheckSubmission(ctx, req.Job); err != nil {
			return jobdb.JobHandle{}, err
		}
		return e.Engine.StartOrAppendJob(ctx, req)
	}
	return Append(ctx, e.Engine, e.Registry, req)
}
func (e WorkflowEngine) StartOrAppendJobRef(ctx context.Context, req jobdb.StartOrAppendJob) (jobref.Ref, error) {
	h, err := e.StartOrAppendJob(ctx, req)
	if err != nil {
		return jobref.Ref{}, err
	}
	return jobref.FromHandle(h)
}
