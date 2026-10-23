package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/colony-2/c2j/pkg/story/internal/model"
	storylive "github.com/colony-2/c2j/pkg/story/live"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func (s *Service) GetRestartContext(ctx context.Context, req model.GetRestartContextRequest) (*model.RestartContext, error) {
	if s.engine == nil {
		return nil, ErrEngineUnavailable
	}
	key := jobdb.JobKey{TenantId: strings.TrimSpace(req.ProjectID), JobId: strings.TrimSpace(req.JobID)}
	if key.TenantId == "" {
		return nil, ErrInvalidProject
	}
	if key.JobId == "" {
		return nil, ErrNotFound
	}
	run, err := s.engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key})
	if err != nil {
		if errors.Is(err, jobdb.ErrJobNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if id := strings.TrimSpace(req.ScopeID); id != "" {
		if req.ChapterOrdinal != 0 {
			return nil, fmt.Errorf("%w: specify chapter ordinal or scope ID, not both", storylive.ErrInvalidRestartBoundary)
		}
		return storylive.InspectRestartContextByScopeID(ctx, s.engine, key, run.Execution, id, s.celProvider, s.rootResolver, s.logger)
	}
	return storylive.InspectRestartContext(ctx, s.engine, key, run.Execution, req.ChapterOrdinal, s.celProvider, s.rootResolver, s.logger)
}
