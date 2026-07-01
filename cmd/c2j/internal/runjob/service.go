package runjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/c2jops"
	"github.com/colony-2/c2j/cmd/c2j/internal/jobutil"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/input"
	coreops "github.com/colony-2/c2j/pkg/ops"
	storylive "github.com/colony-2/c2j/pkg/story/live"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/template/colonycel"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

const (
	exitCodeFailure         = 1
	exitCodeWaitTimeout     = 2
	exitCodeInputRequired   = 3
	exitCodeNotRunnable     = 4
	exitCodeInvalidIdentity = 5
)

func Run(ctx context.Context, opts Options) error {
	if err := opts.Complete(ctx); err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	if err := opts.Validate(); err != nil {
		return exitError{code: exitCodeInvalidIdentity, err: err}
	}

	deps, cleanup, err := buildDeps(ctx, opts)
	if err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	defer cleanup()

	jobKey := jobdb.JobKey{TenantId: opts.TenantID, JobId: opts.JobID}
	renderer := newStoryProgressRenderer(opts.Stdout, "cached", !opts.CI && isTerminalWriter(opts.Stdout))

	if err := replayCachedHistory(ctx, deps, jobKey, renderer, opts.Stderr); err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	renderer.Flush()
	renderer.SetMode("live")

	deadline := time.Now().Add(opts.WaitTimeout)
	for {
		liveRecorder := storylive.NewRecorder(storylive.Options{
			JobKey:   jobKey,
			OnChange: renderer.Render,
		})
		liveWorker := newStoryJobWorker(deps, liveRecorder)

		runnable, err := jobworkflow.GetJobForRun(ctx, deps.runtime, jobworkflow.GetJobForRunRequest{
			JobKey:         jobKey,
			JobWorker:      liveWorker,
			TaskWorkers:    deps.taskWorkers,
			WorkerID:       opts.WorkerID,
			LeaseDuration:  opts.LeaseDuration,
			AwaitThreshold: opts.AwaitThreshold,
			Logger:         slog.Default(),
		})
		if err != nil {
			return exitError{code: exitCodeFailure, err: err}
		}

		if outcome, ok := runnable.Outcome(); ok {
			wait, err := handleOutcome(ctx, opts, deps, jobKey, outcome, deadline)
			if err != nil {
				return err
			}
			if !wait {
				return nil
			}
			continue
		}

		outcome, err := runnable.Run(liveRecorder.Observer())
		renderer.Render(liveRecorder.Finalize(err))
		renderer.Flush()
		if err != nil {
			return exitError{code: exitCodeFailure, err: err}
		}

		wait, err := handleOutcome(ctx, opts, deps, jobKey, outcome, deadline)
		if err != nil {
			return err
		}
		if !wait {
			return nil
		}
	}
}

type runnerDeps struct {
	runtime      jobdb.WorkflowRuntime
	engine       jobworkflow.Engine
	taskWorkers  []jobworkflow.TaskWorker
	celProvider  template.CELOptionsProvider
	rootResolver compiler.RecipeSourceResolver
	inputRuntime *input.Runtime
	stopRegistry func()
	stopRuntime  func() error
}

func buildDeps(ctx context.Context, opts Options) (*runnerDeps, func(), error) {
	handle, err := swfruntime.OpenWorker(ctx, opts.SWFURL, opts.TenantID)
	if err != nil {
		return nil, nil, fmt.Errorf("open JobDB runtime: %w", err)
	}

	c2jops.Register()

	recipeSourceResolver, stopRegistry, err := jobutil.BuildRecipeSourceResolver()
	if err != nil {
		return nil, nil, err
	}

	ctl := &workerworkflow.SWFWorkflowControl{
		Engine:                        handle.Engine,
		PreferRuntimeRecipeResolution: true,
	}
	serviceDeps := coreops.NewServiceDepsBuilder().WithWorkflowControl(ctl).Build()

	activityRegistry, err := workerops.NewActivityRegistry()
	if err != nil {
		if stopRegistry != nil {
			stopRegistry()
		}
		return nil, nil, fmt.Errorf("create activity registry: %w", err)
	}
	activityRegistry.SetDependencies(serviceDeps)

	celProvider := colonycel.NewBuilder(colonycel.Options{})
	workset, err := compiler.NewRecipeWorkerWithOptions(serviceDeps, activityRegistry, compiler.RecipeJobWorkerOptions{
		CELOptionsProvider: celProvider,
		RootSourceResolver: recipeSourceResolver,
	})
	if err != nil {
		if stopRegistry != nil {
			stopRegistry()
		}
		return nil, nil, fmt.Errorf("create recipe worker: %w", err)
	}
	if err := handle.Engine.RegisterWorkers(workset); err != nil {
		if stopRegistry != nil {
			stopRegistry()
		}
		return nil, nil, fmt.Errorf("register workers: %w", err)
	}

	inputRuntime, err := input.NewRuntime(ctl, nil)
	if err != nil {
		if stopRegistry != nil {
			stopRegistry()
		}
		return nil, nil, err
	}

	deps := &runnerDeps{
		runtime:      handle.Runtime,
		engine:       handle.Engine,
		taskWorkers:  taskWorkersFromWorkSet(workset),
		celProvider:  celProvider,
		rootResolver: recipeSourceResolver,
		inputRuntime: inputRuntime,
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
	taskWorkers := make([]jobworkflow.TaskWorker, 0, len(workset.TaskWorkers))
	for _, taskWorker := range workset.TaskWorkers {
		taskWorkers = append(taskWorkers, taskWorker)
	}
	return taskWorkers
}

func newStoryJobWorker(deps *runnerDeps, recorder *storylive.Recorder) jobworkflow.JobWorker {
	return compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{
		CELOptionsProvider:     deps.celProvider,
		RootSourceResolver:     deps.rootResolver,
		OnRecipeLoaded:         recorder.OnRecipeLoaded,
		OnRecipeSourceResolved: recorder.OnRecipeSourceResolved,
		ExecutorFactory:        recorder.ExecutorFactory(),
	})
}

func replayCachedHistory(ctx context.Context, deps *runnerDeps, jobKey jobdb.JobKey, renderer storyProgressRenderer, stderr io.Writer) error {
	run, err := deps.engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: jobKey})
	if err != nil {
		if errors.Is(err, jobdb.ErrJobNotFound) {
			return fmt.Errorf("job %s/%s not found", jobKey.TenantId, jobKey.JobId)
		}
		fmt.Fprintf(stderr, "warning: unable to inspect prior run history: %v\n", err)
		return nil
	}
	if len(run.Attempts) == 0 {
		return nil
	}

	cachedRecorder := storylive.NewRecorder(storylive.Options{
		JobKey:   jobKey,
		OnChange: renderer.Render,
	})
	replayWorker := newStoryJobWorker(deps, cachedRecorder)

	_, err = deps.engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{
		JobKey:    jobKey,
		Observer:  cachedRecorder.Observer(),
		JobWorker: replayWorker,
	})
	renderer.Render(cachedRecorder.Finalize(err))
	renderer.Flush()
	if err == nil {
		return nil
	}

	if errors.Is(err, jobdb.ErrJobNotFound) {
		return fmt.Errorf("job %s/%s not found", jobKey.TenantId, jobKey.JobId)
	}

	var cacheMiss jobworkflow.ReplayCacheMissError
	if errors.As(err, &cacheMiss) {
		return nil
	}
	if strings.Contains(err.Error(), "leaseId is required") || strings.Contains(err.Error(), "lease is required") {
		return nil
	}

	fmt.Fprintf(stderr, "warning: replay unavailable: %v\n", err)
	return nil
}

func handleOutcome(ctx context.Context, opts Options, deps *runnerDeps, jobKey jobdb.JobKey, outcome jobworkflow.JobRunOutcome, deadline time.Time) (bool, error) {
	switch outcome.Status {
	case jobworkflow.JobRunCompleted:
		return false, nil
	case jobworkflow.JobRunFailed:
		jobErr := outcome.JobError
		if jobErr == nil {
			jobErr = fmt.Errorf("job failed")
		}
		return false, exitError{code: exitCodeFailure, err: jobErr}
	case jobworkflow.JobRunSuspended, jobworkflow.JobRunNotLeaseable:
		handled, err := handlePendingInput(ctx, opts, deps.inputRuntime, jobKey)
		if err != nil {
			return false, err
		}
		if handled {
			return true, nil
		}
		if shouldFailNotReady(opts.OnNotReady, outcome) {
			return false, exitError{code: exitCodeNotRunnable, err: fmt.Errorf("job is not runnable: %s", describeBlocking(outcome))}
		}
		if time.Now().After(deadline) {
			return false, exitError{code: exitCodeWaitTimeout, err: fmt.Errorf("timed out waiting: %s", describeBlocking(outcome))}
		}
		fmt.Fprintf(opts.Stdout, "waiting: %s\n", describeBlocking(outcome))
		time.Sleep(opts.PollInterval)
		return true, nil
	default:
		return false, exitError{code: exitCodeFailure, err: fmt.Errorf("unexpected job outcome %s", outcome.Status)}
	}
}

func handlePendingInput(ctx context.Context, opts Options, runtime *input.Runtime, jobKey jobdb.JobKey) (bool, error) {
	details, err := runtime.GetDetails(ctx, jobKey.TenantId, jobKey.JobId)
	if err != nil {
		if errors.Is(err, input.ErrInputNotPending) {
			return false, nil
		}
		return false, exitError{code: exitCodeFailure, err: err}
	}

	switch opts.InputMode {
	case "fail":
		return false, exitError{code: exitCodeInputRequired, err: fmt.Errorf("input required for job %s", jobKey.JobId)}
	case "ops":
		payload := map[string]any{
			"kind":      "input_required",
			"job_id":    jobKey.JobId,
			"tenant_id": jobKey.TenantId,
			"form":      details.Form,
			"blocking":  true,
		}
		if err := json.NewEncoder(opts.Stdout).Encode(payload); err != nil {
			return false, exitError{code: exitCodeFailure, err: err}
		}
		return false, exitError{code: exitCodeInputRequired, err: fmt.Errorf("input required for job %s", jobKey.JobId)}
	default:
		resp, err := promptForInput(opts.Stdin, opts.Stdout, details)
		if err != nil {
			return false, exitError{code: exitCodeFailure, err: err}
		}
		if err := runtime.SubmitResponse(ctx, jobKey.TenantId, jobKey.JobId, resp); err != nil {
			return false, exitError{code: exitCodeFailure, err: err}
		}
		fmt.Fprintf(opts.Stdout, "[live] input submitted for job %s\n", jobKey.JobId)
		return true, nil
	}
}

func shouldFailNotReady(policy string, outcome jobworkflow.JobRunOutcome) bool {
	switch policy {
	case "fail":
		return true
	case "fail-on-lease":
		return outcome.JobStatus != nil && (*outcome.JobStatus == jobdb.JobStatusActive || *outcome.JobStatus == jobdb.JobStatusCrashConcern)
	case "fail-on-pending-jobs":
		return outcome.JobStatus != nil && *outcome.JobStatus == jobdb.JobStatusPendingJobs
	case "fail-on-future":
		return outcome.JobStatus != nil && *outcome.JobStatus == jobdb.JobStatusAwaitingFuture
	case "fail-on-missing-capability":
		return outcome.MissingCapability != nil && strings.TrimSpace(*outcome.MissingCapability) != ""
	default:
		return false
	}
}

func describeBlocking(outcome jobworkflow.JobRunOutcome) string {
	parts := make([]string, 0, 4)
	if outcome.JobStatus != nil {
		parts = append(parts, "status="+string(*outcome.JobStatus))
	}
	if outcome.MissingCapability != nil && strings.TrimSpace(*outcome.MissingCapability) != "" {
		parts = append(parts, "missing_capability="+*outcome.MissingCapability)
	}
	if len(outcome.WaitForJobIDs) > 0 {
		parts = append(parts, "wait_for="+strings.Join(outcome.WaitForJobIDs, ","))
	}
	if outcome.NextNeed != nil && strings.TrimSpace(*outcome.NextNeed) != "" {
		parts = append(parts, "next_need="+*outcome.NextNeed)
	}
	if len(parts) == 0 {
		return string(outcome.Status)
	}
	return strings.Join(parts, " ")
}
