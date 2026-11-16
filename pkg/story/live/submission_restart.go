package live

import (
	"fmt"
	"sort"

	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// SubmissionRestartScope exposes the stored submission boundary, including
// histories where execution has not begun.
func SubmissionRestartScope(run jobdb.GetJobRunResponse) (model.RestartScope, error) {
	if run.Job.JobType != "recipe" || run.Start.Ordinal != 0 {
		return model.RestartScope{}, fmt.Errorf("%w: no restartable job submission", ErrInvalidRestartBoundary)
	}
	return model.RestartScope{
		ScopeID: starter.SubmissionRestartScopeID, ChapterOrdinal: 0,
		Title: "Job submission", StartedAt: run.Start.CreatedAt,
	}, nil
}

// InspectSubmissionRestartContext reads only the submitted data. Recipe defaults,
// cell context, and hook policy have not been resolved at this boundary.
func InspectSubmissionRestartContext(key jobdb.JobKey, run jobdb.GetJobRunResponse) (*model.RestartContext, error) {
	scope, err := SubmissionRestartScope(run)
	if err != nil {
		return nil, err
	}
	if run.Start.Input == nil {
		return nil, fmt.Errorf("%w: submission input is missing", ErrRestartContextUnavailable)
	}
	var submitted *struct {
		Inputs  map[string]any `json:"inputs"`
		Context map[string]any `json:"context"`
	}
	if err := jsonexact.Unmarshal(run.Start.Input.Data, &submitted); err != nil {
		return nil, fmt.Errorf("%w: read submission input: %w", ErrRestartContextUnavailable, err)
	}
	if submitted == nil {
		return nil, fmt.Errorf("%w: submission input is null", ErrRestartContextUnavailable)
	}
	if submitted.Context == nil {
		submitted.Context = map[string]any{}
	}
	if submitted.Inputs == nil {
		submitted.Inputs = map[string]any{}
	}
	result := &model.RestartContext{
		ProjectID: key.TenantId, JobID: key.JobId, ScopeID: scope.ScopeID,
		ChapterOrdinal: scope.ChapterOrdinal, RedactedPaths: []string{},
	}
	result.Job = redactRestartValue("", "/job", submitted.Context, &result.RedactedPaths).(map[string]any)
	result.Visible = redactRestartValue("", "/visible", map[string]any{"inputs": submitted.Inputs}, &result.RedactedPaths).(map[string]any)
	sort.Strings(result.RedactedPaths)
	return result, nil
}
