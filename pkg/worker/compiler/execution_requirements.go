package compiler

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type ExecutionHandoff = execution.Handoff

// Each invocation reconstructs requirements from input and durable results.
// Published payloads are scheduling hints and are never restored into replay.
type executionSession struct {
	ctx       jobworkflow.ExecutionContext
	base      execution.Requirements
	node      execution.Requirements
	overrides execution.Requirements
	stage     func(jobdb.JobKey, execution.Demand)
	guarded   bool
}

func (j recipeJobWorker) executionSession(ctx jobworkflow.ExecutionContext, r recipe.Recipe, initial *execution.Submission) (*executionSession, error) {
	// Permission enforcement is checked by each live task worker. Embedders may
	// register wrapped workers separately; read-only replay needs no capabilities.
	s := &executionSession{ctx: ctx, stage: j.stageExecution, guarded: j.stageExecution != nil && j.taskGuardConfigured}
	if initial != nil {
		if err := initial.Validate(); err != nil {
			return nil, err
		}
		s.overrides = execution.Overlay(initial.JobRequirements, execution.Requirements{})
	}
	if base := r.GetMetadata().Execution; base != nil {
		var err error
		s.base, err = base.Normalize()
		if err != nil {
			return nil, err
		}
	}
	if (recipe.HasExecutionConstraints(r) || needsAllocation(s.effective())) && !s.guarded {
		return nil, fmt.Errorf("execution requirements require an execution-aware runtime and guarded task workers")
	}
	s.stageCurrent()
	return s, nil
}

func needsAllocation(r execution.Requirements) bool {
	r.Packages = nil
	return !r.Empty()
}

func (s *executionSession) effective() execution.Requirements {
	return execution.Overlay(execution.Overlay(s.base, s.node), s.overrides)
}
func (s *executionSession) stageCurrent() {
	if s.stage != nil {
		s.stage(s.ctx.GetJobKey(), execution.Demand{SchemaVersion: execution.DemandSchemaVersion, RecipeResolution: "resolved", Effective: s.effective()})
	}
}
func (s *executionSession) stageNode(needs execution.Requirements) {
	s.node = execution.Overlay(needs, execution.Requirements{})
	s.stageCurrent()
}

type executionControlError struct{ error }

func (e *executionControlError) Unwrap() error { return e.error }
func isExecutionControlError(err error) bool {
	var control *executionControlError
	return errors.As(err, &control)
}

// apply reconstructs a durable directive even when its task result was cached.
// Only the guarded worker for unfinished work checks allocation and may yield.
func (s *executionSession) apply(patch execution.Requirements) error {
	p, err := patch.Normalize()
	if err != nil {
		return &executionControlError{err}
	}
	if p.Empty() {
		return &executionControlError{fmt.Errorf("execution change requires a nonempty patch")}
	}
	if needsAllocation(p) && !s.guarded {
		return &executionControlError{fmt.Errorf("execution requirements require an execution-aware runtime and guarded task workers")}
	}
	s.overrides = execution.Overlay(s.overrides, p)
	s.stageCurrent()
	return nil
}

// ExecutionHandoffFromOutcome decodes only this run's acknowledged environment
// handoff. Ordinary waits and unrelated Yield metadata are not environment changes.
func ExecutionHandoffFromOutcome(outcome jobworkflow.JobRunOutcome) (*ExecutionHandoff, error) {
	if outcome.Handoff == nil || len(outcome.Handoff.Metadata) == 0 {
		return nil, nil
	}
	var event ExecutionHandoff
	if err := json.Unmarshal(outcome.Handoff.Metadata, &event); err != nil {
		return nil, fmt.Errorf("decode execution handoff: %w", err)
	}
	if event.Kind != "environment_required" {
		return nil, nil
	}
	return &event, nil
}
