package compiler

import (
	"context"
	"strings"

	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/metricshistory"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type taskMetricsKey struct{}

// ObserveTaskMetrics creates consumption state for a custom recipe job worker.
// NewRecipeJobWorker installs it automatically. Custom ExecutionWorkers that call
// ExecuteRecipe should set their ObserveTaskResults factory to this function.
// Use this hook alone; feeding it again through replay/live listeners duplicates
// notifications. Whole-job retries share state; new activations rebuild it.
func ObserveTaskMetrics(ctx context.Context) (context.Context, func(jobworkflow.TaskEndEvent)) {
	a := metrics.NewAccumulator()
	return context.WithValue(ctx, taskMetricsKey{}, a), func(event jobworkflow.TaskEndEvent) {
		channels := metricshistory.Decode(nil)
		if event.Output != nil {
			if raw, err := event.Output.GetData(); err == nil {
				channels = metricshistory.Decode(raw)
			} else {
				channels.Runtime.Status = "unavailable"
				channels.Producer.Status = "unavailable"
			}
		}
		// TaskEndEvent identifies the material task attempt. Only remove its
		// final attempt segment; slashes in quoted site names remain intact.
		id := event.InvocationID
		if i := strings.LastIndex(id, "/attempt:"); i >= 0 {
			id = id[:i]
		}
		for _, c := range []struct {
			name string
			data metrics.Channel
		}{{"runtime", channels.Runtime}, {"producer", channels.Producer}} {
			a.Consume(metrics.Record{Tenant: event.JobKey.TenantId, Job: event.JobKey.JobId, Task: id, Attempt: event.AttemptNumber, Ordinal: event.Ordinal, Channel: c.name, ChannelReport: c.data})
		}
	}
}

func taskMetrics(ctx context.Context) *metrics.Accumulator {
	a, _ := ctx.Value(taskMetricsKey{}).(*metrics.Accumulator)
	return a
}
