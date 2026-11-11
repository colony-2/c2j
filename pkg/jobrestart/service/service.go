// Package service shares restart selection and inspection between CLI and
// authorized worker helpers. Discovery reads history without replay.
package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story/live"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/template/colonycel"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type Service struct {
	Engine    jobworkflow.Engine
	Tenant    string
	Authorize func(context.Context, string) error
	// Submit is deliberately separate: a history/replay grant is not a restart grant.
	Submit   func(context.Context, jobdb.JobKey, string, *task.ContextPatch, starter.RestartOptions) (jobref.Ref, error)
	Provider template.CELOptionsProvider
	Resolver compiler.RecipeSourceResolver
}

func (s *Service) Restart(ctx context.Context, q Request) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Authorize != nil {
		if err := s.Authorize(ctx, q.JobID); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(q.JobID) == "" || strings.TrimSpace(s.Tenant) == "" {
		return nil, fmt.Errorf("job and tenant are required")
	}
	if q.Operation != "points" {
		if err := q.Selector.Validate(); err != nil {
			return nil, err
		}
	}
	if err := jobrestart.ValidatePatch(q.Patch); err != nil {
		return nil, err
	}
	if s.Engine == nil {
		return nil, fmt.Errorf("restart history/replay capability unavailable")
	}
	key := jobdb.JobKey{TenantId: s.Tenant, JobId: q.JobID}
	run, err := s.Engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: q.Operation == "context"})
	if err != nil {
		return nil, err
	}
	points := &Points{ProjectID: s.Tenant, JobID: q.JobID, RestartScopes: live.RecordedRestartScopes(run)}
	if q.Operation == "points" {
		return &Response{Points: points}, nil
	}
	point, err := Select(points.RestartScopes, q.Selector)
	if err != nil {
		return nil, err
	}
	switch q.Operation {
	case "context":
		if point.ScopeID == starter.SubmissionRestartScopeID {
			snapshot, err := live.InspectSubmissionRestartContext(key, run)
			return &Response{Context: snapshot}, err
		}
		provider := s.Provider
		if provider == nil {
			provider = colonycel.NewBuilder(colonycel.Options{})
		}
		resolver := s.Resolver
		if resolver == nil {
			resolver = compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{})
		}
		snapshot, err := live.InspectRestartContextByScopeID(ctx, s.Engine, key, run.Execution, point.ScopeID, provider, resolver)
		return &Response{Context: snapshot}, err
	case "submit":
		if point.ScopeID == starter.SubmissionRestartScopeID && q.Patch != nil && len(*q.Patch) > 0 {
			return nil, fmt.Errorf("submission restart does not support context patches; select a recipe scope")
		}
		if s.Submit == nil {
			return nil, fmt.Errorf("authorized restart submission capability unavailable; this worker only supports restart inspection")
		}
		ref, err := s.Submit(ctx, key, point.ScopeID, q.Patch, starter.RestartOptions{Goal: q.Goal, RequestID: q.RequestID})
		if err != nil {
			return nil, err
		}
		return &Response{Ref: &ref}, nil
	default:
		return nil, fmt.Errorf("unknown restart operation %q", q.Operation)
	}
}
func Select(points []jobrestart.RestartScope, selector Selector) (jobrestart.RestartScope, error) {
	if err := selector.Validate(); err != nil {
		return jobrestart.RestartScope{}, err
	}
	var matches []jobrestart.RestartScope
	for _, point := range points {
		if (selector.Chapter != nil && point.ChapterOrdinal == *selector.Chapter) || (selector.Chapter == nil && point.ScopeID == selector.Scope) {
			matches = append(matches, point)
		}
	}
	if len(matches) != 1 {
		return jobrestart.RestartScope{}, fmt.Errorf("%w: selector matches %d boundaries; use a unique scope ID", live.ErrInvalidRestartBoundary, len(matches))
	}
	if matches[0].UnavailableReason != "" {
		return jobrestart.RestartScope{}, fmt.Errorf("%w: %s", live.ErrInvalidRestartBoundary, matches[0].UnavailableReason)
	}
	return matches[0], nil
}

type Selector = jobrestart.Selector
type Points = jobrestart.Points
type Request = jobrestart.Request
type Response = jobrestart.Response

// TaskSubmitter keeps source metadata reads on the tenant-scoped engine and
// sends the atomic restart through the live operation's restricted lease.
type TaskSubmitter struct {
	jobworkflow.Engine
	Registry jobdb.JobSchemaRegistry
}

func (s TaskSubmitter) SubmitRestartJobRef(ctx context.Context, req jobdb.SubmitRestartJobRequest) (jobref.Ref, error) {
	access := permissions.FromContext(ctx)
	if access == nil {
		return jobref.Ref{}, fmt.Errorf("authorized restart submission capability unavailable")
	}
	handle, err := (jobdbschema.Submitter{RestartSubmitter: leaseSubmitter{access}, Registry: s.Registry}).SubmitRestartJob(ctx, req)
	if err != nil {
		return jobref.Ref{}, err
	}
	return jobref.FromHandle(handle)
}
func (s TaskSubmitter) Submit(ctx context.Context, key jobdb.JobKey, scope string, patch *task.ContextPatch, opts starter.RestartOptions) (jobref.Ref, error) {
	return starter.RestartRecipeJob(ctx, s, key, scope, patch, opts)
}

type leaseSubmitter struct{ access *permissions.Access }

func (s leaseSubmitter) SubmitRestartJob(ctx context.Context, req jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	ref, err := s.access.Restart(ctx, req)
	if err != nil {
		return jobdb.JobHandle{}, err
	}
	return jobdb.JobHandle{JobKey: ref.Key(), GoalID: ref.GoalID, Cell: ref.Cell, CellID: ref.CellID, CellRepo: ref.CellRepo}, nil
}
