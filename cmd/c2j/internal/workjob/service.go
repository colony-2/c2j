package workjob

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"sort"
	"sync"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/c2jops"
	"github.com/colony-2/c2j/cmd/c2j/internal/jobutil"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/template/colonycel"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

const (
	exitCodeFailure        = 1
	exitCodeInvalidOptions = 2
)

func Run(ctx context.Context, opts Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := opts.Complete(ctx); err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	if err := opts.Validate(); err != nil {
		return exitError{code: exitCodeInvalidOptions, err: err}
	}

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	var outputMu sync.Mutex
	var outputErr error
	deps, cleanup, err := buildWorkerDeps(runCtx, workerBuildOptions{
		TenantID: opts.TenantID, SWFURL: opts.SWFURL, Concurrency: opts.Concurrency, AwaitThreshold: opts.AwaitThreshold, Allocation: opts.Allocation,
		OnHandoff: func(event compiler.ExecutionHandoff) {
			outputMu.Lock()
			defer outputMu.Unlock()
			outputErr = json.NewEncoder(opts.Stdout).Encode(event)
			stop()
		},
	})
	if err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	defer cleanup()

	if _, err := fmt.Fprintf(opts.Stdout, "working tenant=%s jobdb=%s concurrency=%d\n", opts.TenantID, opts.JobDBURI, opts.Concurrency); err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}

	deps.engine.Run(runCtx)
	outputMu.Lock()
	eventErr := outputErr
	outputMu.Unlock()
	if eventErr != nil {
		return eventErr
	}
	if err := ctx.Err(); err != nil && err != context.Canceled {
		return exitError{code: exitCodeFailure, err: err}
	}
	return nil
}

type workerDeps struct {
	engine       jobworkflow.Engine
	stopRegistry func()
	stopRuntime  func() error
}

func buildDeps(ctx context.Context, opts Options) (*workerDeps, func(), error) {
	return buildWorkerDeps(ctx, workerBuildOptions{
		Allocation:     opts.Allocation,
		TenantID:       opts.TenantID,
		SWFURL:         opts.SWFURL,
		Concurrency:    opts.Concurrency,
		AwaitThreshold: opts.AwaitThreshold,
	})
}

type workerBuildOptions struct {
	Allocation     execution.Allocation
	OnHandoff      func(compiler.ExecutionHandoff)
	TenantID       string
	SWFURL         string
	Concurrency    int
	AwaitThreshold time.Duration
	WrapRuntime    func(jobdb.WorkflowRuntime) jobdb.WorkflowRuntime
}

func buildWorkerDeps(ctx context.Context, opts workerBuildOptions) (*workerDeps, func(), error) {
	handle, err := swfruntime.Open(ctx, opts.SWFURL)
	if err != nil {
		return nil, nil, fmt.Errorf("open JobDB runtime: %w", err)
	}
	runtime := handle.Runtime
	if opts.Allocation.SchemaVersion == 0 {
		opts.Allocation.SchemaVersion = execution.SchemaVersion
	}
	executionRuntime := executionruntime.New(runtime, opts.Allocation, opts.OnHandoff)
	runtime = executionRuntime
	if opts.WrapRuntime != nil {
		runtime = opts.WrapRuntime(runtime)
	}

	cleanupOnErr := func(err error, stopRegistry func()) (*workerDeps, func(), error) {
		if stopRegistry != nil {
			stopRegistry()
		}
		_ = handle.Cleanup()
		return nil, nil, err
	}

	c2jops.Register()

	recipeSourceResolver, stopRegistry, err := jobutil.BuildRecipeSourceResolver()
	if err != nil {
		return cleanupOnErr(err, nil)
	}

	ctl := &workerworkflow.SWFWorkflowControl{
		Engine:                        handle.Engine,
		PreferRuntimeRecipeResolution: true,
	}
	serviceDeps := coreops.NewServiceDepsBuilder().WithWorkflowControl(ctl).Build()

	activityRegistry, err := workerops.NewActivityRegistry()
	if err != nil {
		return cleanupOnErr(fmt.Errorf("create activity registry: %w", err), stopRegistry)
	}
	activityRegistry.SetDependencies(serviceDeps)

	celProvider := colonycel.NewBuilder(colonycel.Options{})
	workset, err := compiler.NewRecipeWorkerWithOptions(serviceDeps, activityRegistry, compiler.RecipeJobWorkerOptions{
		Allocation:         opts.Allocation,
		StageExecution:     executionRuntime.Stage,
		WrapTaskWorker:     executionRuntime.WrapTaskWorker,
		OnExecutionHandoff: opts.OnHandoff,
		CELOptionsProvider: celProvider,
		RootSourceResolver: recipeSourceResolver,
	})
	if err != nil {
		return cleanupOnErr(fmt.Errorf("create recipe worker: %w", err), stopRegistry)
	}

	builder := jobworkflow.NewEngineBuilder().
		WithRuntime(runtime).
		WithWorkerTenantId(opts.TenantID).
		WithMaxActive(opts.Concurrency)
	if opts.AwaitThreshold > 0 {
		builder = builder.WithAwaitRecycleThreshold(opts.AwaitThreshold)
	}
	builder = builder.PlusWorkers(workset.JobWorker, taskWorkersFromWorkSet(workset)...)
	engine, err := builder.BuildEngine()
	if err != nil {
		return cleanupOnErr(fmt.Errorf("build worker engine: %w", err), stopRegistry)
	}
	schemaRegistry, ok := handle.Runtime.(jobdb.JobSchemaRegistry)
	if !ok {
		return cleanupOnErr(fmt.Errorf("jobdb schema registry is unavailable"), stopRegistry)
	}
	engine = jobdbschema.WorkflowEngine{
		Engine:   engine,
		Registry: schemaRegistry,
	}
	ctl.Engine = engine

	deps := &workerDeps{
		engine:       engine,
		stopRegistry: stopRegistry,
		stopRuntime:  handle.Cleanup,
	}
	return deps, func() {
		if deps.stopRuntime != nil {
			_ = deps.stopRuntime()
		}
		if deps.stopRegistry != nil {
			deps.stopRegistry()
		}
	}, nil
}

func taskWorkersFromWorkSet(workset *jobworkflow.WorkSet) []jobworkflow.TaskWorker {
	if workset == nil || len(workset.TaskWorkers) == 0 {
		return nil
	}
	taskTypes := make([]string, 0, len(workset.TaskWorkers))
	for taskType := range workset.TaskWorkers {
		taskTypes = append(taskTypes, taskType)
	}
	sort.Strings(taskTypes)
	taskWorkers := make([]jobworkflow.TaskWorker, 0, len(taskTypes))
	for _, taskType := range taskTypes {
		taskWorkers = append(taskWorkers, workset.TaskWorkers[taskType])
	}
	return taskWorkers
}
