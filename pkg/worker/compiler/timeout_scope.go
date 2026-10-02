package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type timeoutJobContext struct {
	inner           jobworkflow.JobContext
	deadline        time.Time
	declaredTimeout time.Duration
	limit           time.Duration
	label           string
	durable         bool
}

type executionTimeoutLimiter interface {
	executionTimeoutLimit() time.Duration
}

func (t *timeoutJobContext) ClientPayload() json.RawMessage {
	return t.inner.ClientPayload()
}

func (t *timeoutJobContext) ClientPayloadRevision() int64 {
	return t.inner.ClientPayloadRevision()
}

func (t *timeoutJobContext) Yield(ctx context.Context, req jobdb.RescheduleExecutionRequest) error {
	if err := t.checkDeadline(); err != nil {
		return err
	}
	return t.inner.Yield(ctx, req)
}

func withExecutionTimeout(ctx jobworkflow.JobContext, timeout time.Duration, label string) jobworkflow.JobContext {
	if timeout <= 0 || ctx == nil {
		return ctx
	}
	limit := timeout
	if innerLimit := activeExecutionTimeoutLimit(ctx); innerLimit > 0 {
		limit = minPositiveDuration(limit, innerLimit)
	}
	return &timeoutJobContext{
		inner:           ctx,
		deadline:        time.Now().Add(timeout),
		declaredTimeout: timeout,
		limit:           limit,
		label:           label,
	}
}

func activeExecutionTimeoutLimit(ctx jobworkflow.JobContext) time.Duration {
	if limiter, ok := ctx.(executionTimeoutLimiter); ok {
		return limiter.executionTimeoutLimit()
	}
	return 0
}

func minPositiveDuration(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

func (t *timeoutJobContext) GetJobKey() jobdb.JobKey {
	return t.inner.GetJobKey()
}

func (t *timeoutJobContext) Logger() *slog.Logger {
	return t.inner.Logger()
}

func (t *timeoutJobContext) AwaitJobs(jobIds ...string) error {
	if err := t.checkDeadline(); err != nil {
		return err
	}
	err := t.inner.AwaitJobs(jobIds...)
	if err != nil {
		if time.Now().After(t.deadline) {
			return t.wrapTimeout(err)
		}
		return err
	}
	return t.checkDeadline()
}

func (t *timeoutJobContext) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobKey, error) {
	if err := t.checkDeadline(); err != nil {
		return jobdb.JobKey{}, err
	}
	key, err := t.inner.SubmitJob(ctx, submit)
	if err != nil {
		if time.Now().After(t.deadline) {
			return key, t.wrapTimeout(err)
		}
		return key, err
	}
	return key, t.checkDeadline()
}

func (t *timeoutJobContext) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	if err := t.checkDeadline(); err != nil {
		return jobdb.JobKey{}, err
	}
	key, err := t.inner.SubmitRestartJob(ctx, restart)
	if err != nil {
		if time.Now().After(t.deadline) {
			return key, t.wrapTimeout(err)
		}
		return key, err
	}
	return key, t.checkDeadline()
}

func (t *timeoutJobContext) AwaitDuration(waitFor jobdb.Duration) error {
	if err := t.checkDeadline(); err != nil {
		return err
	}
	remaining := time.Until(t.deadline)
	effectiveWait := time.Duration(waitFor)
	if effectiveWait > remaining {
		effectiveWait = remaining
	}
	err := t.inner.AwaitDuration(jobdb.Duration(effectiveWait))
	if err != nil {
		if time.Now().After(t.deadline) {
			return t.wrapTimeout(err)
		}
		return err
	}
	return t.checkDeadline()
}

func (t *timeoutJobContext) executionTimeoutLimit() time.Duration {
	return t.limit
}

func (t *timeoutJobContext) DoTask(policy jobdb.RunPolicy, taskType string, data jobdb.TaskData) (jobdb.TaskData, error) {
	// Control checkpoints must replay even after an enclosing scope expires.
	if taskType == TimeoutCheckpointTaskType {
		return t.inner.DoTask(policy, taskType, data)
	}
	if t.durable {
		at, err := timeoutCheckpoint(t.inner, "task", t.label, t.declaredTimeout)
		if err != nil {
			return nil, err
		}
		// A cached admission allows the corresponding cached task to replay.
		// JobDB checks the total limit before executing an uncached local task.
		// Do not reject cached output merely because recovery happened later.
		return t.doTaskAt(policy, taskType, data, at)
	}
	if err := t.checkDeadline(); err != nil {
		return nil, err
	}
	policy = clampRunPolicyToDeadline(policy, t.deadline)
	out, err := t.inner.DoTask(policy, taskType, data)
	if err != nil {
		if time.Now().After(t.deadline) {
			return out, t.wrapTimeout(err)
		}
		return out, err
	}
	if err := t.checkDeadline(); err != nil {
		return out, err
	}
	return out, nil
}

// All enclosing scopes share one durable admission timestamp. Inserting a
// separate checkpoint per parent would move the task's input-chapter clock and
// could extend a child's budget by the time spent recording parent checkpoints.
func (t *timeoutJobContext) doTaskAt(policy jobdb.RunPolicy, taskType string, data jobdb.TaskData, at time.Time) (jobdb.TaskData, error) {
	if !at.Before(t.deadline) {
		return nil, t.timeoutError()
	}
	return doTaskAt(t.inner, clampRunPolicyToDeadlineAt(policy, t.deadline, at), taskType, data, at)
}

func doTaskAt(ctx jobworkflow.JobContext, policy jobdb.RunPolicy, taskType string, data jobdb.TaskData, at time.Time) (jobdb.TaskData, error) {
	if scoped, ok := ctx.(interface {
		doTaskAt(jobdb.RunPolicy, string, jobdb.TaskData, time.Time) (jobdb.TaskData, error)
	}); ok {
		return scoped.doTaskAt(policy, taskType, data, at)
	}
	return ctx.DoTask(policy, taskType, data)
}

func (t *timeoutJobContext) checkDeadline() error {
	if time.Now().Before(t.deadline) {
		return nil
	}
	return t.timeoutError()
}

func (t *timeoutJobContext) wrapTimeout(err error) error {
	return fmt.Errorf("%w: %v", t.timeoutError(), err)
}

func (t *timeoutJobContext) timeoutError() error {
	return fmt.Errorf("%s: %w: %w", t.timeoutMessage(), context.DeadlineExceeded,
		jobdb.NewTimeoutError("job", t.declaredTimeout, jobdb.TimeoutScopeTotal, nil, false))
}

func (t *timeoutJobContext) timeoutMessage() string {
	label := t.label
	if label == "" {
		label = "execution"
	}
	if t.declaredTimeout > 0 {
		return fmt.Sprintf("%s timed out after %s", label, t.declaredTimeout)
	}
	return fmt.Sprintf("%s timed out", label)
}

func clampRunPolicyToDeadline(policy jobdb.RunPolicy, deadline time.Time) jobdb.RunPolicy {
	return clampRunPolicyToDeadlineAt(policy, deadline, time.Now())
}

func clampRunPolicyToDeadlineAt(policy jobdb.RunPolicy, deadline, startedAt time.Time) jobdb.RunPolicy {
	remaining := deadline.Sub(startedAt)
	if remaining <= 0 {
		// JobDB interprets zero as an unlimited timeout, not an expired one.
		remaining = time.Nanosecond
	}
	if policy.TotalTimeout != nil {
		existing := time.Duration(*policy.TotalTimeout)
		if existing > 0 && existing < remaining {
			return policy
		}
	}
	timeout := jobdb.Duration(remaining)
	policy.TotalTimeout = &timeout
	return policy
}

var _ jobworkflow.JobContext = (*timeoutJobContext)(nil)
