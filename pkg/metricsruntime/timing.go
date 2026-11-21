// Package metricsruntime instruments live c2j attempts. Replayed results never enter it.
package metricsruntime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/colony-2/c2j/internal/preparedtest"
	"github.com/colony-2/c2j/pkg/metrics"
)

type timingKey struct{}
type Timing struct {
	mu        sync.Mutex
	runtime   *metrics.Collector
	producer  *metrics.Collector
	taskType  string
	now       func() time.Time
	start     time.Time
	execution bool
	phases    map[string]string
	closeCPU  func() metrics.CPUCoverage
	cpu       metrics.CPUCoverage
	closed    bool
}

func Begin(ctx context.Context, taskType string, check func(any) error) (context.Context, *Timing) {
	return begin(ctx, taskType, check, preparedtest.Clock(ctx), processSampler)
}
func begin(ctx context.Context, taskType string, check func(any) error, now func() time.Time, sampler *Sampler) (context.Context, *Timing) {
	t := &Timing{runtime: metrics.NewRuntimeCollector(check), producer: metrics.NewCollector(check), taskType: taskType, now: now, start: now(), phases: map[string]string{"preparation": "entered", "execution": "not_started"}}
	t.closeCPU = sampler.Start()
	ctx = context.WithValue(ctx, timingKey{}, t)
	ctx = metrics.WithCollector(ctx, t.producer)
	return ctx, t
}
func Current(ctx context.Context) *Timing { t, _ := ctx.Value(timingKey{}).(*Timing); return t }

// Execution starts exactly at the worker's body invocation, after input hydration.
func Execution(ctx context.Context) {
	if t := Current(ctx); t != nil {
		t.mu.Lock()
		defer t.mu.Unlock()
		if !t.execution && !t.closed {
			t.observePhase("preparation", "succeeded")
			t.execution = true
			t.start = t.now()
			t.phases["execution"] = "entered"
		}
	}
}
func outcome(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	if err != nil {
		return "failed"
	}
	return "succeeded"
}
func (t *Timing) observePhase(phase, status string) {
	t.runtime.Observe("c2j_task_duration_seconds", "s", map[string]string{"task_type": t.taskType, "phase": phase, "outcome": status}, t.now().Sub(t.start).Seconds())
	t.phases[phase] = status
}
func (t *Timing) Finish(err error) metrics.Channels {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed {
		phase := "preparation"
		if t.execution {
			phase = "execution"
		}
		t.observePhase(phase, outcome(err))
		cpu := t.closeCPU()
		if cpu.MeasuredSeconds > 0 {
			labels := map[string]string{"source": "worker_process"}
			t.runtime.AddFloat("c2j_process_cpu_seconds_total", "s", labels, cpu.ProcessSeconds)
			t.runtime.AddFloat("c2j_process_cpu_observed_seconds_total", "s", labels, cpu.MeasuredSeconds)
		}
		t.cpu = cpu
		t.closed = true
	}
	r := t.runtime.Snapshot()
	r.Phases = t.phases
	r.CPU = &t.cpu
	return metrics.Channels{Runtime: r, Producer: t.producer.Snapshot()}
}

// Component returns an idempotent span closure. Labels are engine-controlled enums.
func Component(ctx context.Context, component, environment string) func(error, bool) {
	t := Current(ctx)
	if t == nil {
		return func(error, bool) {}
	}
	switch component {
	case "workspace", "environment", "configuration", "secrets", "input":
	default:
		return func(error, bool) {}
	}
	switch environment {
	case "nix", "python", "npm", "uv", "native", "none":
	default:
		environment = "none"
	}
	start := t.now()
	var once sync.Once
	return func(err error, reused bool) {
		once.Do(func() {
			status := "prepared"
			if reused {
				status = "reused"
			}
			if err != nil {
				status = outcome(err)
			}
			t.runtime.Observe("c2j_task_preparation_component_seconds", "s", map[string]string{"component": component, "environment": environment, "outcome": status}, t.now().Sub(start).Seconds())
		})
	}
}
func Request(ctx context.Context, operation string) func(error) {
	t := Current(ctx)
	if t == nil || (operation != "git.pull" && operation != "git.fetch") {
		return func(error) {}
	}
	start := t.now()
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			if err != nil && ctx.Err() != nil {
				err = ctx.Err()
			}
			t.runtime.Observe("c2j_request_duration_seconds", "s", map[string]string{"operation": operation, "outcome": outcome(err)}, t.now().Sub(start).Seconds())
		})
	}
}

// Discard closes sampling without producing a timing or producer envelope.
func (t *Timing) Discard() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed {
		t.closeCPU()
		t.closed = true
	}
}
