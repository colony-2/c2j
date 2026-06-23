package workjob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const defaultLeaseDuration = 60 * time.Second

type RunOneOptions struct {
	TenantID       string
	SWFURL         string
	LeaseDuration  time.Duration
	AwaitThreshold time.Duration
	WorkingDir     string
	Stdout         io.Writer
	Stderr         io.Writer
}

func (o *RunOneOptions) Complete(ctx context.Context) error {
	if strings.TrimSpace(o.SWFURL) == "" {
		o.SWFURL = strings.TrimSpace(os.Getenv(defaults.SWFEnv))
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		o.SWFURL = defaults.SWFURL
	}
	if o.LeaseDuration == 0 {
		o.LeaseDuration = defaultLeaseDuration
	}
	if o.AwaitThreshold == 0 {
		o.AwaitThreshold = defaultAwaitThreshold
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if strings.TrimSpace(o.WorkingDir) == "" {
		if cwd, err := os.Getwd(); err == nil {
			o.WorkingDir = cwd
		}
	}
	if strings.TrimSpace(o.WorkingDir) != "" {
		if absPath, err := filepath.Abs(o.WorkingDir); err == nil {
			o.WorkingDir = absPath
		}
	}
	if strings.TrimSpace(o.TenantID) == "" {
		o.TenantID = strings.TrimSpace(os.Getenv(defaults.TenantEnv))
	}
	if strings.TrimSpace(o.TenantID) == "" {
		tenantID, err := defaults.ResolveTenantID(ctx, o.WorkingDir)
		if err != nil {
			return err
		}
		o.TenantID = tenantID
	}
	return nil
}

func (o RunOneOptions) Validate() error {
	if strings.TrimSpace(o.TenantID) == "" {
		return fmt.Errorf("--tenant-id is required (or %s, or project self.tenant_id/self.repo)", defaults.TenantEnv)
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		return fmt.Errorf("--swf-url is required (or %s)", defaults.SWFEnv)
	}
	if err := validateSWFURL(o.SWFURL); err != nil {
		return err
	}
	if o.LeaseDuration <= 0 {
		return fmt.Errorf("--lease-duration must be > 0")
	}
	if o.AwaitThreshold < 0 {
		return fmt.Errorf("--await-threshold must be >= 0")
	}
	return nil
}

func RunOne(ctx context.Context, opts RunOneOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := opts.Complete(ctx); err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	if err := opts.Validate(); err != nil {
		return exitError{code: exitCodeInvalidOptions, err: err}
	}

	var once *runOneRuntime
	deps, cleanup, err := buildWorkerDeps(ctx, workerBuildOptions{
		TenantID:       opts.TenantID,
		SWFURL:         opts.SWFURL,
		Concurrency:    1,
		AwaitThreshold: opts.AwaitThreshold,
		WrapRuntime: func(runtime jobdb.WorkflowRuntime) jobdb.WorkflowRuntime {
			once = newRunOneRuntime(runtime, opts.LeaseDuration)
			return once
		},
	})
	if err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	defer cleanup()
	if once == nil {
		return exitError{code: exitCodeFailure, err: fmt.Errorf("run any runtime wrapper was not installed")}
	}

	runCtx, stop := context.WithCancel(ctx)
	once.setCancel(stop)
	deps.engine.Run(runCtx)
	stop()

	state := once.state()
	if state.err != nil {
		return exitError{code: exitCodeFailure, err: state.err}
	}
	if err := ctx.Err(); err != nil && !state.stopped {
		return exitError{code: exitCodeFailure, err: err}
	}
	if !state.found {
		if _, err := fmt.Fprintln(opts.Stdout, "no jobs found"); err != nil {
			return exitError{code: exitCodeFailure, err: err}
		}
		return nil
	}
	if !state.finalized {
		return exitError{code: exitCodeFailure, err: fmt.Errorf("job %s stopped without completing or rescheduling", state.jobKey)}
	}

	action := state.action
	if action == "" {
		action = "processed"
	}
	status := state.status
	if status == "" {
		status = "unknown"
	}
	if _, err := fmt.Fprintf(opts.Stdout, "%s job=%s status=%s\n", action, state.jobKey, status); err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	return nil
}

type runOneRuntime struct {
	jobdb.WorkflowRuntime

	leaseDuration time.Duration

	mu        sync.Mutex
	cancel    context.CancelFunc
	polled    bool
	found     bool
	finalized bool
	stopped   bool
	jobKey    jobdb.JobKey
	action    string
	status    string
	err       error
}

type runOneState struct {
	found     bool
	finalized bool
	stopped   bool
	jobKey    jobdb.JobKey
	action    string
	status    string
	err       error
}

func newRunOneRuntime(runtime jobdb.WorkflowRuntime, leaseDuration time.Duration) *runOneRuntime {
	return &runOneRuntime{
		WorkflowRuntime: runtime,
		leaseDuration:   leaseDuration,
	}
}

func (r *runOneRuntime) setCancel(cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancel = cancel
}

func (r *runOneRuntime) PollWork(ctx context.Context, req jobdb.PollWorkRequest) ([]jobdb.ExecutionLease, error) {
	r.mu.Lock()
	if r.polled {
		if !r.finalized && r.err == nil {
			r.err = fmt.Errorf("job %s stopped without completing or rescheduling", r.jobKey)
		}
		r.stopLocked()
		r.mu.Unlock()
		return nil, nil
	}
	r.polled = true
	r.mu.Unlock()

	req.Limit = 1
	if r.leaseDuration > 0 {
		req.LeaseDuration = r.leaseDuration
	}
	leases, err := r.WorkflowRuntime.PollWork(ctx, req)
	if err != nil {
		r.mu.Lock()
		r.err = fmt.Errorf("poll work: %w", err)
		r.stopLocked()
		r.mu.Unlock()
		return nil, err
	}
	if len(leases) == 0 {
		r.mu.Lock()
		r.stopLocked()
		r.mu.Unlock()
		return nil, nil
	}

	lease := leases[0]
	jobKey := lease.Job().JobKey
	r.mu.Lock()
	r.found = true
	r.jobKey = jobKey
	r.mu.Unlock()

	return []jobdb.ExecutionLease{&runOneLease{ExecutionLease: lease, runtime: r}}, nil
}

func (r *runOneRuntime) markFinalized(action string, status string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized {
		return
	}
	r.finalized = true
	r.action = action
	r.status = status
	if err != nil {
		r.err = err
	}
	r.stopLocked()
}

func (r *runOneRuntime) stopLocked() {
	r.stopped = true
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *runOneRuntime) state() runOneState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return runOneState{
		found:     r.found,
		finalized: r.finalized,
		stopped:   r.stopped,
		jobKey:    r.jobKey,
		action:    r.action,
		status:    r.status,
		err:       r.err,
	}
}

type runOneLease struct {
	jobdb.ExecutionLease
	runtime *runOneRuntime
}

func (l *runOneLease) LeaseToken() string {
	if tokenLease, ok := l.ExecutionLease.(interface{ LeaseToken() string }); ok {
		return tokenLease.LeaseToken()
	}
	return ""
}

func (l *runOneLease) LeaseWorkerID() string {
	if workerLease, ok := l.ExecutionLease.(interface{ LeaseWorkerID() string }); ok {
		return workerLease.LeaseWorkerID()
	}
	return ""
}

func (l *runOneLease) Complete(ctx context.Context, req jobdb.CompleteExecutionRequest) error {
	err := l.ExecutionLease.Complete(ctx, req)
	l.runtime.markFinalized("completed", req.Status, err)
	return err
}

func (l *runOneLease) Reschedule(ctx context.Context, req jobdb.RescheduleExecutionRequest) error {
	err := l.ExecutionLease.Reschedule(ctx, req)
	status := "rescheduled"
	if req.NextNeed != "" {
		status = req.NextNeed
	}
	l.runtime.markFinalized("rescheduled", status, err)
	return err
}
