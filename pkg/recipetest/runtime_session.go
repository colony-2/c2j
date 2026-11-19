package recipetest

import (
	"context"
	"fmt"

	"github.com/colony-2/c2j/internal/dispatchtest"
	"github.com/colony-2/c2j/internal/scenariotest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type workerSession struct {
	engine jobworkflow.Engine
	cancel context.CancelFunc
	done   chan struct{}
}

func (f *runtimeFixture) buildSession(rt caseRuntime, tenant string, collector *observationCollector) (*workerSession, error) {
	var workerRuntime jobdb.ConsumerRuntime = rt
	if f.c.Runtime.Controls != nil {
		workerRuntime = controlledRuntime{rt}
		if f.scenario != nil {
			workerRuntime = scenarioRuntime{rt, f}
			if _, ok := rt.(jobdb.LeaseDelegator); ok {
				workerRuntime = delegatedScenarioRuntime{scenarioRuntime{rt, f}}
			}
		}
	}
	scopedRuntime := executionruntime.New(workerRuntime, execution.Allocation{SchemaVersion: 1})
	builder := jobworkflow.NewEngineBuilder().WithRuntime(scopedRuntime).WithWorkerTenantId(tenant)
	if f.scenario != nil {
		builder.WithMaxActive(32)
	}
	engine, err := builder.BuildEngine()
	if err != nil {
		return nil, err
	}
	engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}
	f.ctl = &workerworkflow.SWFWorkflowControl{Engine: engine}
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(f.ctl).Build()
	registry, err := workerops.NewActivityRegistry()
	if err != nil {
		return nil, err
	}
	for name, registration := range registry.GetAll() {
		reg := registration
		invoke := reg.Step.Invoke
		reg.Step.Invoke = func(deps coreops.OpDependencies, ctx context.Context, in map[string]any) (map[string]any, error) {
			return f.invoke(deps, ctx, reg.Metadata.Type, invoke, in)
		}
		registry.UpdateRegistration(name, reg)
	}
	var executor compiler.RecipeExecutor
	if collector != nil {
		collector.opener = rt
		executor = observationExecutor{collector: collector}
	}
	workers, err := compiler.NewRecipeWorkerWithOptions(deps, registry, compiler.RecipeJobWorkerOptions{
		Executor:           executor,
		RootSourceResolver: compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{}),
		Allocation:         scopedRuntime.Allocation, WrapTaskWorker: func(worker jobworkflow.TaskWorker) jobworkflow.TaskWorker {
			guarded := scopedRuntime.WrapTaskWorker(worker)
			if f.c.Runtime.Controls != nil {
				return dispatchTaskWorker{guarded, f}
			}
			return guarded
		}, StageExecution: scopedRuntime.Stage,
	})
	if err == nil {
		err = engine.RegisterWorkers(workers)
	}
	if err != nil {
		return nil, err
	}
	return &workerSession{engine: engine}, nil
}
func (f *runtimeFixture) startSession(ctx context.Context, s *workerSession) error {
	if s.done != nil {
		return fmt.Errorf("worker session already started")
	}
	local, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})
	if f.c.Runtime.Controls != nil {
		local = dispatchtest.WithObserver(local, f.beginDispatch)
		if f.scenario != nil {
			local = scenariotest.WithObserver(local, scenariotest.Observer{Submission: f.beginSubmission, Workspace: f.associateWorkspace})
		}
		if f.c.Runtime.Controls.Snapshot != nil {
			local = dispatchtest.WithCleanupRetainer(local, func(cleanup func()) {
				f.mu.Lock()
				defer f.mu.Unlock()
				f.retainedCleanups = append(f.retainedCleanups, cleanup)
			})
		}
	}
	if f.scenario != nil {
		f.mu.Lock()
		f.scenario.stopped = false
		f.scenario.session++
		f.scenario.report.Sessions = append(f.scenario.report.Sessions, ScenarioSession{ID: f.scenario.session, Kind: "started"})
		f.mu.Unlock()
	}
	go func() { defer close(s.done); s.engine.Run(local) }()
	return nil
}
func (f *runtimeFixture) stopSession(ctx context.Context, s *workerSession) error {
	if s == nil || s.cancel == nil {
		return fmt.Errorf("no running worker session")
	}
	if f.scenario != nil {
		f.nativeMu.Lock()
		f.mu.Lock()
		f.scenario.stopped = true
		f.mu.Unlock()
		f.nativeMu.Unlock()
	}
	s.cancel()
	// Engine.Run joins all lease runners, which stop their keepalives before
	// returning. Storage is never closed merely because cancellation was sent.
	select {
	case <-s.done:
		s.cancel = nil
	case <-ctx.Done():
		return fmt.Errorf("worker drain: %w", ctx.Err())
	}
	if f.scenario != nil {
		f.mu.Lock()
		f.scenario.report.Sessions = append(f.scenario.report.Sessions, ScenarioSession{ID: f.scenario.session, Kind: "joined"})
		f.mu.Unlock()
	}
	return nil
}
