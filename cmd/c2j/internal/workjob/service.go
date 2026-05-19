package workjob

import (
	"context"
	"fmt"
	"sort"

	"github.com/colony-2/c2j/cmd/c2j/internal/c2jops"
	"github.com/colony-2/c2j/cmd/c2j/internal/jobutil"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/template/colonycel"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/swf-go/pkg/swf"
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

	deps, cleanup, err := buildDeps(ctx, opts)
	if err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	defer cleanup()

	if _, err := fmt.Fprintf(opts.Stdout, "working tenant=%s swf_url=%s concurrency=%d\n", opts.TenantID, opts.SWFURL, opts.Concurrency); err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}

	deps.engine.Run(ctx)
	if err := ctx.Err(); err != nil && err != context.Canceled {
		return exitError{code: exitCodeFailure, err: err}
	}
	return nil
}

type workerDeps struct {
	engine       swf.SWFEngine
	stopRegistry func()
	stopRuntime  func() error
}

func buildDeps(ctx context.Context, opts Options) (*workerDeps, func(), error) {
	handle, err := swfruntime.Open(ctx, opts.SWFURL)
	if err != nil {
		return nil, nil, fmt.Errorf("open SWF runtime: %w", err)
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
		CELOptionsProvider: celProvider,
		RootSourceResolver: recipeSourceResolver,
	})
	if err != nil {
		return cleanupOnErr(fmt.Errorf("create recipe worker: %w", err), stopRegistry)
	}

	builder := swf.NewEngineBuilder().
		WithRuntime(handle.Runtime).
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

func taskWorkersFromWorkSet(workset *swf.WorkSet) []swf.TaskWorker {
	if workset == nil || len(workset.TaskWorkers) == 0 {
		return nil
	}
	taskTypes := make([]string, 0, len(workset.TaskWorkers))
	for taskType := range workset.TaskWorkers {
		taskTypes = append(taskTypes, taskType)
	}
	sort.Strings(taskTypes)
	taskWorkers := make([]swf.TaskWorker, 0, len(taskTypes))
	for _, taskType := range taskTypes {
		taskWorkers = append(taskWorkers, workset.TaskWorkers[taskType])
	}
	return taskWorkers
}
