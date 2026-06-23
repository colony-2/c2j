package compiler

import (
	"context"
	"fmt"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"log/slog"
	"time"
)

type timeoutJobContext struct {
	inner           jobworkflow.JobContext
	deadline        time.Time
	declaredTimeout time.Duration
	limit           time.Duration
	label           string
}

type executionTimeoutLimiter interface {
	executionTimeoutLimit() time.Duration
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

func (t *timeoutJobContext) checkDeadline() error {
	if time.Now().Before(t.deadline) {
		return nil
	}
	return t.timeoutError()
}

func (t *timeoutJobContext) wrapTimeout(err error) error {
	return fmt.Errorf("%s: %w: %v", t.timeoutMessage(), context.DeadlineExceeded, err)
}

func (t *timeoutJobContext) timeoutError() error {
	return fmt.Errorf("%s: %w", t.timeoutMessage(), context.DeadlineExceeded)
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
	remaining := time.Until(deadline)
	if remaining < 0 {
		remaining = 0
	}
	if policy.TotalTimeout != nil {
		existing := time.Duration(*policy.TotalTimeout)
		if existing >= 0 && existing < remaining {
			return policy
		}
	}
	timeout := jobdb.Duration(remaining)
	policy.TotalTimeout = &timeout
	return policy
}

var _ jobworkflow.JobContext = (*timeoutJobContext)(nil)
