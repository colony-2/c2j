package compiler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type ExecutionHandoff struct {
	Kind       string               `json:"kind"`
	JobKey     jobdb.JobKey         `json:"job"`
	Demand     execution.Demand     `json:"demand"`
	Allocation execution.Allocation `json:"allocation"`
	Mismatches []execution.Mismatch `json:"mismatches,omitempty"`
	Published  bool                 `json:"published"`
}

type executionSession struct {
	ctx        jobworkflow.JobContext
	demand     execution.Demand
	allocation execution.Allocation
	onHandoff  func(ExecutionHandoff)
	stage      func(jobdb.JobKey, execution.Demand)
	checkpoint int
}

func (j recipeJobWorker) executionSession(ctx jobworkflow.JobContext, r recipe.Recipe, initial *execution.Demand) (*executionSession, error) {
	a := j.allocation
	if a.SchemaVersion == 0 {
		a.SchemaVersion = execution.SchemaVersion
	}
	a, err := a.Normalize()
	if err != nil {
		return nil, err
	}
	overrides := execution.Requirements{}
	if initial != nil {
		if err := initial.Validate(); err != nil {
			return nil, err
		}
		overrides = initial.JobRequirements
	}
	digest, err := recipe.ExecutionDigest(r)
	if err != nil {
		return nil, err
	}
	d, err := execution.Initial(r.GetMetdata().Execution, digest, overrides)
	if err != nil {
		return nil, err
	}
	if initial != nil && initial.RecipeResolution == "resolved" && initial.RecipeDigest != d.RecipeDigest {
		return nil, fmt.Errorf("initial execution recipe digest disagrees with pinned recipe")
	}
	current, err := execution.PayloadDemand(ctx.ClientPayload())
	if err != nil {
		return nil, err
	}
	if current != nil {
		if current.RecipeDigest != d.RecipeDigest || !reflect.DeepEqual(current.RecipeBase, d.RecipeBase) {
			return nil, fmt.Errorf("published execution demand disagrees with pinned recipe")
		}
		d = *current
	}
	s := &executionSession{ctx: ctx, demand: d, allocation: a, onHandoff: j.onExecutionHandoff, stage: j.stageExecution}
	s.stageCurrent()
	m, err := execution.Compare(d.Effective, a)
	if err != nil {
		return nil, err
	}
	if len(m) > 0 {
		return nil, s.yield(ctx, current == nil, m)
	}
	return s, nil
}

func (s *executionSession) stageCurrent() {
	if s.stage != nil {
		s.stage(s.ctx.GetJobKey(), s.demand)
	}
}

type executionControlError struct{ error }

func (e *executionControlError) Unwrap() error { return e.error }

func isExecutionControlError(err error) bool {
	var control *executionControlError
	return errors.As(err, &control)
}

func (s *executionSession) suspend(ctx jobworkflow.JobContext, site string, patch execution.Requirements) (result error) {
	defer func() {
		if result != nil {
			result = &executionControlError{result}
		}
	}()
	p, err := patch.Normalize()
	if err != nil {
		return err
	}
	if p.Empty() {
		return fmt.Errorf("execution suspension requires a nonempty patch")
	}
	s.checkpoint++
	key := fmt.Sprintf("%d:%s", s.checkpoint, site)
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	if prior, ok := s.demand.Checkpoints[key]; ok {
		if prior != hash {
			return fmt.Errorf("execution checkpoint %q changed during replay", key)
		}
		return nil
	}
	if s.demand.Checkpoints == nil {
		s.demand.Checkpoints = map[string]string{}
	}
	s.demand.Checkpoints[key] = hash
	s.demand.JobRequirements = execution.Overlay(s.demand.JobRequirements, p)
	b := execution.Requirements{}
	if s.demand.RecipeBase != nil {
		b = *s.demand.RecipeBase
	}
	s.demand.Effective = execution.Overlay(b, s.demand.JobRequirements)
	s.demand.Revision++
	m, err := execution.Compare(s.demand.Effective, s.allocation)
	if err != nil {
		return err
	}
	return s.yield(ctx, true, m)
}

func (s *executionSession) yield(ctx jobworkflow.JobContext, publish bool, mismatches []execution.Mismatch) error {
	req := jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: starter.RecipeJobType}}
	if publish {
		s.demand.LastAllocation = &s.allocation
		raw, err := execution.PayloadWithDemand(s.ctx.ClientPayload(), s.demand)
		if err != nil {
			return err
		}
		revision := s.ctx.ClientPayloadRevision()
		req.ClientPayloadUpdate = &jobdb.ClientPayloadUpdate{Mode: "reset", Value: raw, ExpectedRevision: &revision}
	}
	// Yield stops the goroutine on success. Notify only after it has returned
	// through its deferred stack; an error/uncertain response is not success.
	accepted := true
	defer func() {
		if p := recover(); p != nil {
			panic(p)
		}
		if accepted && s.onHandoff != nil {
			s.onHandoff(ExecutionHandoff{Kind: "environment_required", JobKey: s.ctx.GetJobKey(), Demand: s.demand, Allocation: s.allocation, Mismatches: mismatches, Published: publish})
		}
	}()
	err := ctx.Yield(context.Background(), req)
	accepted = false
	if err != nil {
		return err
	}
	return fmt.Errorf("execution yield returned without stopping invocation")
}
