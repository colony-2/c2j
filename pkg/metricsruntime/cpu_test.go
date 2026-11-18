package metricsruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/stretchr/testify/require"
)

func TestProcessCPUPairedIntervalsAndIdle(t *testing.T) {
	now := time.Unix(0, 0)
	cpu := 0.
	reads := 0
	sampler := NewSampler(func() time.Time { return now }, func() (float64, error) { reads++; return cpu, nil })
	a := sampler.Start()
	now = now.Add(30 * time.Second)
	cpu = 30
	first := a()
	now = now.Add(10 * time.Second)
	sampler.Tick()
	require.Equal(t, 2, reads)
	b := sampler.Start()
	now = now.Add(60 * time.Second)
	cpu = 45
	second := b()
	require.Equal(t, 45., first.ProcessSeconds+second.ProcessSeconds)
	require.Equal(t, 90., first.MeasuredSeconds+second.MeasuredSeconds)
	require.Equal(t, .5, (first.ProcessSeconds+second.ProcessSeconds)/(first.MeasuredSeconds+second.MeasuredSeconds))
	require.Equal(t, first, a()) // Closing twice cannot sample or add a second interval.
	now = now.Add(time.Second)
	start := sampler.Start()
	now = now.Add(time.Second)
	cpu += 3
	third := start()
	require.Equal(t, 3., third.ProcessSeconds/third.MeasuredSeconds)
}
func TestCPUOverlapAndReadRecovery(t *testing.T) {
	now := time.Unix(0, 0)
	cpu := 0.
	var readErr error
	sampler := NewSampler(func() time.Time { return now }, func() (float64, error) { return cpu, readErr })
	a := sampler.Start()
	now = now.Add(10 * time.Second)
	cpu = 10
	b := sampler.Start()
	now = now.Add(20 * time.Second)
	cpu = 20
	overlapB := b()
	require.Equal(t, 0., overlapB.MeasuredSeconds)
	require.Equal(t, 20., overlapB.ExcludedSeconds["concurrent_tasks"])
	now = now.Add(10 * time.Second)
	cpu = 25
	sampler.Tick()
	now = now.Add(time.Second)
	readErr = errors.New("unavailable")
	sampler.Tick()
	now = now.Add(time.Second)
	readErr = nil
	cpu = 26
	sampler.Tick()
	now = now.Add(time.Second)
	cpu = 25
	sampler.Tick() // reset must discard both values.
	now = now.Add(time.Second)
	cpu = 26
	coverage := a()
	require.Equal(t, 21., coverage.MeasuredSeconds)
	require.Equal(t, 16., coverage.ProcessSeconds)
	require.Equal(t, 20., coverage.ExcludedSeconds["concurrent_tasks"])
	require.Equal(t, 2., coverage.ExcludedSeconds["read_error"])
	require.Equal(t, 1., coverage.ExcludedSeconds["reset"])
	require.Empty(t, sampler.active)
}
func TestCPUUnsupportedAndBoundedLongTask(t *testing.T) {
	now := time.Unix(0, 0)
	s := NewSampler(func() time.Time { return now }, func() (float64, error) { return 0, ErrCPUUnsupported })
	end := s.Start()
	now = now.Add(time.Second)
	c := end()
	require.Equal(t, "unsupported", c.Availability)
	require.Zero(t, c.MeasuredSeconds)
	cpu := 0.
	s = NewSampler(func() time.Time { return now }, func() (float64, error) { return cpu, nil })
	end = s.Start()
	for i := 0; i < 1000; i++ {
		now = now.Add(time.Minute)
		cpu += 30
		s.Tick()
	}
	now = now.Add(time.Second)
	cpu += .5
	c = end()
	require.Equal(t, 60001., c.MeasuredSeconds)
	require.Equal(t, 30000.5, c.ProcessSeconds)
	require.Empty(t, c.ExcludedSeconds)
}
func TestExplicitPhaseAndNestedComponentBoundaries(t *testing.T) {
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	sampler := NewSampler(clock, func() (float64, error) { return 0, ErrCPUUnsupported })
	ctx, timing := begin(context.Background(), "native:run", nil, clock, sampler)
	workspace := Component(ctx, "workspace", "none")
	now = now.Add(time.Second)
	env := Component(ctx, "environment", "python")
	now = now.Add(2 * time.Second)
	env(nil, true)
	workspace(nil, false)
	now = now.Add(time.Second)
	Execution(ctx)
	now = now.Add(5 * time.Second)
	request := Request(ctx, "git.pull")
	now = now.Add(time.Second)
	request(errors.New("failed"))
	request(nil)
	channels := timing.Finish(nil)
	require.Equal(t, "succeeded", channels.Runtime.Phases["preparation"])
	require.Equal(t, "succeeded", channels.Runtime.Phases["execution"])
	records := []metrics.Record{{Task: `/scope:"$job"#1/attempt:1/task:"run"#1`, Attempt: 1, Ordinal: 1, Channel: "runtime", ChannelReport: channels.Runtime}}
	reduced, e := metrics.Reduce(records, metrics.Query{}, 1)
	require.NoError(t, e)
	phases := reduced.Snapshot.Series["c2j_task_duration_seconds"]
	require.Len(t, phases, 2)
	for _, p := range phases {
		if p.Labels["phase"] == "preparation" {
			require.Equal(t, 4., p.Sum)
		} else {
			require.Equal(t, 6., p.Sum)
		}
	}
	require.Len(t, reduced.Snapshot.Series["c2j_task_preparation_component_seconds"], 2)
	require.Equal(t, int64(1), reduced.Snapshot.Series["c2j_request_duration_seconds"][0].Count)
	require.Equal(t, "failed", reduced.Snapshot.Series["c2j_request_duration_seconds"][0].Labels["outcome"])
	require.Equal(t, channels, timing.Finish(errors.New("late")))
}
func TestPreparationFailureHasNoExecutionObservation(t *testing.T) {
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	sampler := NewSampler(clock, func() (float64, error) { return 0, ErrCPUUnsupported })
	_, timing := begin(context.Background(), "workspace", nil, clock, sampler)
	now = now.Add(time.Second)
	channels := timing.Finish(context.Canceled)
	require.Equal(t, "not_started", channels.Runtime.Phases["execution"])
	require.Equal(t, "canceled", channels.Runtime.Phases["preparation"])
	require.Len(t, channels.Runtime.Report.Mutations, 1)
	require.Empty(t, sampler.active)
}
