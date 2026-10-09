package runjob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	storylive "github.com/colony-2/c2j/pkg/story/live"
	"github.com/colony-2/jobdb/pkg/jobdb"
	remoteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// RunWithLease consumes a remote capability from a protected file or stdin.
// The invocation ends on completion or rescheduling and never claims a lease.
func RunWithLease(ctx context.Context, opts Options, leaseFile string) error {
	if opts.InputMode == "" {
		opts.InputMode = "ops"
	}
	if err := opts.Complete(ctx); err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	if err := opts.Validate(); err != nil {
		return exitError{code: exitCodeInvalidIdentity, err: err}
	}
	if opts.InputMode != "ops" && opts.InputMode != "fail" {
		return exitError{code: exitCodeFailure, err: fmt.Errorf("with-lease supports --input-mode ops or fail; respond to pending input separately")}
	}
	if !strings.HasPrefix(opts.SWFURL, "http://") && !strings.HasPrefix(opts.SWFURL, "https://") {
		return exitError{code: exitCodeFailure, err: fmt.Errorf("with-lease requires a remote http(s) JobDB; library callers can execute an already-held embedded lease")}
	}
	encoded, err := readLeaseInput(leaseFile, opts.Stdin)
	if err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	capability, err := remoteruntime.DecodeLeaseCapability(encoded)
	clear(encoded)
	if err != nil {
		return exitError{code: exitCodeFailure, err: fmt.Errorf("decode supplied lease: %w", err)}
	}
	deps, cleanup, err := buildDeps(ctx, opts)
	if err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	defer cleanup()
	if deps.importLease == nil {
		return exitError{code: exitCodeFailure, err: jobdb.ErrLeaseRenewalUnsupported}
	}
	lease, err := deps.importLease(ctx, capability)
	if err != nil {
		return exitError{code: exitCodeFailure, err: newSuppliedLeaseValidationError(opts.JobDBURI, err)}
	}
	jobKey := jobdb.JobKey{TenantId: opts.TenantID, JobId: opts.JobID}
	if lease.Job().JobKey != jobKey {
		return exitError{code: exitCodeInvalidIdentity, err: fmt.Errorf("supplied lease does not match --jobdb tenant and --job-id: %w", jobdb.ErrExecutionLeaseLost)}
	}
	renderer := newStoryProgressRenderer(opts.Stdout, "live", !opts.CI && isTerminalWriter(opts.Stdout))
	recorder := storylive.NewRecorder(storylive.Options{JobKey: jobKey, OnChange: renderer.Render})
	// The live runner already replays durable history. Avoid doing a separate
	// cached replay before its heartbeat has started.
	outcome, err := deps.executionRuntime.RunWithLease(ctx, lease, jobworkflow.GetJobForRunRequest{
		JobKey: jobKey, JobWorker: newStoryJobWorker(deps, recorder),
		TaskWorkers: deps.taskWorkers, WorkerID: opts.WorkerID,
		AwaitThreshold: opts.AwaitThreshold, Logger: slog.Default(),
	}, recorder.Observer())
	renderer.Render(recorder.Finalize(err))
	renderer.Flush()
	// Lease loss must not be hidden by a concurrently reported handoff.
	if err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	if event := deps.handoff(); event != nil {
		return json.NewEncoder(opts.Stdout).Encode(event)
	}
	switch outcome.Status {
	case jobworkflow.JobRunCompleted:
		return nil
	case jobworkflow.JobRunFailed:
		jobErr := outcome.JobError
		if jobErr == nil {
			jobErr = fmt.Errorf("job failed")
		}
		return exitError{code: exitCodeFailure, err: jobErr}
	case jobworkflow.JobRunSuspended:
		if _, err := handlePendingInput(ctx, opts, deps.inputRuntime, jobKey); err != nil {
			return err
		}
		return json.NewEncoder(opts.Stdout).Encode(map[string]any{
			"kind": "job_suspended", "job_id": jobKey.JobId, "tenant_id": jobKey.TenantId,
			"outcome": outcome,
		})
	default:
		return exitError{code: exitCodeFailure, err: fmt.Errorf("unexpected supplied-lease outcome %s", outcome.Status)}
	}
}
