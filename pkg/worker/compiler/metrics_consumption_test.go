package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/metricshistory"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// This compares return/listener/replay paths and accepts c2j's synchronous
// consumption hook. Broader recipe view acceptance lives in the connected fixtures.
// A return-path accumulator replays successful results without a history read,
// but cannot recover internally retried reports or distinguish a catch's
// replacement output from the original task result. ReplayObserver already
// sees those reports at the right boundary; the public live listener is async.
func TestMetricsReplayConsumptionBoundary(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend).WorkflowRuntime
			ctx := celltest.Context(context.Background())
			live := &metricsConsumptionObserver{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
			replay := &metricsConsumptionObserver{}
			var builds, runs atomic.Int32
			var prefixes []int64
			var committedPrefixes []int64
			var incrementalPrefixes, incrementalCatch []int64
			var catchPrefixes, listenerPrefixes []int64
			bodyVisits, matches := 0, 0
			replaying := false
			worker := wf.ExecutionWorker{JobType: "metrics-consumption", ObserveTaskResults: ObserveTaskMetrics, Execute: func(exec wf.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
				if !replaying {
					defer live.unblock()
				}
				return exec.DoScope("outer", wf.ScopeOptions{}, func(scope wf.ExecutionContext) (jobdb.JobData, error) {
					bodyVisits++
					// This is the same wrapper used for replaying workspace snapshots.
					forwarder := newThinPackForwardingJobContext(scope)
					observer := live
					if replaying {
						observer = replay
					}
					var returned int64
					prefixes = append(prefixes, returned)
					committedPrefixes = append(committedPrefixes, observer.total())
					incrementalPrefixes = append(incrementalPrefixes, consumedMetricTokens(scope.Context()))
					for _, site := range []string{"a", "retry", "caught", "d"} {
						call := wf.TaskCall{Site: site, TaskType: "metric-report", ContractVersion: 1, Command: json.RawMessage(`{}`)}
						if site == "retry" {
							call.Policy.Retry = jobdb.RetryPolicy{MaximumAttempts: 2, InitialInterval: jobdb.Duration(time.Millisecond)}
						}
						if site == "caught" {
							call.Catch = []wf.CatchRule{{ID: "replace", Match: func(error) bool { matches++; return true }, Handle: func(wf.ExecutionContext, error) (wf.CatchResult, error) {
								catchPrefixes = append(catchPrefixes, returned)
								incrementalCatch = append(incrementalCatch, consumedMetricTokens(scope.Context()))
								observer := live
								if replaying {
									observer = replay
								}
								listenerPrefixes = append(listenerPrefixes, observer.total())
								// A replacement is not another material task report, even
								// if it happens to have the same JSON envelope shape.
								return wf.CatchResult{Disposition: wf.CatchHandled, Output: metricsConsumptionOutput(1000)}, nil
							}}}
						}
						out, err := forwarder.DoTask(call, func(context.Context) (jobdb.TaskData, error) {
							builds.Add(1)
							return jobdb.NewTaskData(site)
						})
						if err != nil {
							return nil, err
						}
						returned += metricsConsumptionTokens(out)
						prefixes = append(prefixes, returned)
						committedPrefixes = append(committedPrefixes, observer.total())
						incrementalPrefixes = append(incrementalPrefixes, consumedMetricTokens(scope.Context()))
						if site == "a" && !replaying {
							// Hold the first asynchronous listener callback while
							// orchestration consumes later results and runs a catch.
							select {
							case <-live.entered:
							case <-time.After(5 * time.Second):
								return nil, fmt.Errorf("live listener did not start")
							}
						}
					}
					return jobdb.NewTaskData(returned)
				})
			}}
			task := metricsConsumptionTask{runs: &runs}
			key := probeSubmit(t, rt, worker)
			runnable, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: []wf.TaskWorker{task}, WorkerID: "metrics-boundary", LeaseDuration: time.Minute})
			require.NoError(t, err)
			outcome, err := runnable.Run(live)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
			select {
			case <-live.done:
			case <-time.After(5 * time.Second):
				t.Fatal("live listener did not finish")
			}
			wantPrefixes := []int64{0, 3, 10, 1010, 1023}
			require.Equal(t, wantPrefixes, prefixes)
			require.Equal(t, []int64{0, 3, 15, 26, 39}, incrementalPrefixes)
			require.Equal(t, []int64{26}, incrementalCatch)
			require.Equal(t, []int64{0, 0, 0, 0, 0}, committedPrefixes, "the live listener may still be queued at every consumer boundary")
			require.Equal(t, []int64{10}, catchPrefixes, "DoTask returns neither the failed retry's 5 nor the caught failure's 11")
			require.Equal(t, []int64{0}, listenerPrefixes, "the live listener can lag behind the catch callback")
			require.EqualValues(t, 4, builds.Load())
			require.EqualValues(t, 5, runs.Load())
			require.Equal(t, 1, bodyVisits)
			require.Equal(t, 1, matches)
			require.Equal(t, int64(39), live.total(), "the drained listener does receive all material attempt reports")

			stored, err := metricshistory.Read(ctx, rt, key, metrics.Query{Name: "fixture_tokens_total", Deltas: true})
			require.NoError(t, err)
			require.Equal(t, int64(39), stored.Snapshot.Series["fixture_tokens_total"][0].Value)
			require.Len(t, live.records(), 5)
			for _, report := range stored.Reports {
				if report.Channel != "producer" {
					continue
				}
				found := false
				for _, event := range live.records() {
					if event.InvocationID == report.Task+"/attempt:"+strconv.Itoa(report.Attempt) {
						found = true
						require.Equal(t, report.Ordinal, event.Ordinal)
					}
				}
				require.True(t, found, "missing committed observer event for %s attempt %d", report.Task, report.Attempt)
			}
			before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			replaying = true
			prefixes = nil
			committedPrefixes = nil
			incrementalPrefixes = nil
			incrementalCatch = nil
			engine, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(worker, task).BuildEngine()
			require.NoError(t, err)
			replayed, err := engine.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: key, Observer: replay})
			require.NoError(t, err)
			require.JSONEq(t, string(outcome.Output.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
			require.Equal(t, wantPrefixes, prefixes, "normal return-path state is reconstructed without a full-history scan")
			require.Equal(t, []int64{0, 3, 15, 26, 39}, incrementalPrefixes, "c2j rebuilds through the synchronous hook alone")
			require.Equal(t, []int64{26}, incrementalCatch)
			require.Equal(t, []int64{0, 3, 15, 26, 39}, committedPrefixes, "synchronous replay accumulation never includes a later stored result")
			require.Equal(t, []int64{10, 10}, catchPrefixes)
			require.Equal(t, []int64{0, 26}, listenerPrefixes, "replay observer is synchronous before catch handling, including the failed retry")
			require.Equal(t, 2, bodyVisits, "scope bodies are reconstructed; they are not skipped with completed builders")
			require.Equal(t, 1, matches, "recorded catch selection skips the predicate on replay")
			require.EqualValues(t, 4, builds.Load(), "completed input builders are skipped")
			require.EqualValues(t, 5, runs.Load(), "task bodies never rerun")
			require.Equal(t, int64(39), replay.total())
			require.Equal(t, live.records(), replay.records(), "existing events already preserve attempt identity, result ordinal, failures and output")
			after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			require.Equal(t, before, after, "reconstruction makes no history writes")
		})
	}
}

// Consumer state for job totals must outlive an individual JobWorker.Run:
// whole-job retries call Run again within the same execution/replay. The
// existing replay observer already spans those attempts without a history read.
func TestMetricsConsumptionAcrossJobAttempts(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend).WorkflowRuntime
			ctx := celltest.Context(context.Background())
			observer := &metricsConsumptionObserver{}
			var builds, runs atomic.Int32
			var enteringTotals, consumedEntering, consumedFinishing []int64
			worker := wf.ExecutionWorker{JobType: "repro", ObserveTaskResults: ObserveTaskMetrics, Execute: func(exec wf.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
				enteringTotals = append(enteringTotals, observer.total())
				consumedEntering = append(consumedEntering, consumedMetricTokens(exec.Context()))
				defer func() { consumedFinishing = append(consumedFinishing, consumedMetricTokens(exec.Context())) }()
				return exec.DoTask(wf.TaskCall{Site: "usage", TaskType: "metric-report", ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) {
					builds.Add(1)
					return jobdb.NewTaskData("job-retry")
				})
			}}
			task := metricsConsumptionTask{runs: &runs}
			key := submitTimeoutRepro(t, rt, "metrics-job-attempts", jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 2, InitialInterval: jobdb.Duration(time.Millisecond)}})
			runnable, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: []wf.TaskWorker{task}, WorkerID: "metrics-attempts", LeaseDuration: time.Minute})
			require.NoError(t, err)
			outcome, err := runnable.Run(nil)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
			require.Equal(t, []int64{0, 0}, enteringTotals, "a fresh consumer without observation has no previous-attempt totals")
			require.Equal(t, []int64{0, 5}, consumedEntering)
			require.Equal(t, []int64{5, 12}, consumedFinishing)
			consumedEntering = nil
			consumedFinishing = nil
			enteringTotals = nil
			engine, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(worker, task).BuildEngine()
			require.NoError(t, err)
			_, err = engine.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: key, Observer: observer})
			require.NoError(t, err)
			require.Equal(t, []int64{0, 5}, enteringTotals, "the synchronous observer spans JobWorker.Run attempts at the consumed boundary")
			require.Equal(t, int64(12), observer.total())
			require.Equal(t, []int64{0, 5}, consumedEntering)
			require.Equal(t, []int64{5, 12}, consumedFinishing)
			require.EqualValues(t, 2, builds.Load())
			require.EqualValues(t, 2, runs.Load())
			stored, err := metricshistory.Read(ctx, rt, key, metrics.Query{Name: "fixture_tokens_total"})
			require.NoError(t, err)
			require.Equal(t, int64(12), stored.Snapshot.Series["fixture_tokens_total"][0].Value)
		})
	}
}

type metricsConsumptionTask struct{ runs *atomic.Int32 }

func (metricsConsumptionTask) Name() string { return "metric-report" }
func (m metricsConsumptionTask) Run(ctx wf.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	invocations := m.runs.Add(1)
	var site string
	if err := json.Unmarshal(input.GetDataOrPanic(), &site); err != nil {
		return nil, err
	}
	switch site {
	case "job-retry":
		if invocations == 1 {
			return metricsConsumptionOutput(5), fmt.Errorf("retry whole job")
		}
		return metricsConsumptionOutput(7), nil
	case "a":
		return metricsConsumptionOutput(3), nil
	case "retry":
		if ctx.AttemptNumber == 1 {
			return metricsConsumptionOutput(5), fmt.Errorf("retry once")
		}
		return metricsConsumptionOutput(7), nil
	case "caught":
		return metricsConsumptionOutput(11), fmt.Errorf("caught failure")
	default:
		return metricsConsumptionOutput(13), nil
	}
}

func metricsConsumptionOutput(tokens int64) jobdb.TaskData {
	return jobdb.NewTaskDataOrPanic(coretask.OutputEnvelope{Version: coretask.OutputEnvelopeVersion, Kind: coretask.OutputKindInternalResult, Payload: json.RawMessage(`{}`), Metrics: &metrics.Channels{
		Runtime: metrics.Channel{Status: "absent"},
		Producer: metrics.Channel{Status: "valid", Report: &metrics.Report{Version: metrics.Version, Mutations: []metrics.Mutation{{
			Descriptor: metrics.Descriptor{Name: "fixture_tokens_total", Kind: "counter", NumberType: "int64", Unit: "{token}"}, Op: "add", Value: tokens,
		}}}},
	}})
}

func metricsConsumptionTokens(out jobdb.TaskData) int64 {
	if out == nil {
		return 0
	}
	c := metricshistory.Decode(out.GetDataOrPanic()).Producer
	if c.Report == nil {
		return 0
	}
	return c.Report.Mutations[0].Value.(int64)
}

type metricsConsumptionObserver struct {
	mu      sync.Mutex
	events  []wf.TaskEndEvent
	tokens  int64
	entered chan struct{}
	release chan struct{}
	done    chan struct{}
	first   sync.Once
	close   sync.Once
}

func (*metricsConsumptionObserver) OnJobStart(wf.JobStartEvent)   {}
func (*metricsConsumptionObserver) OnTaskStart(wf.TaskStartEvent) {}
func (o *metricsConsumptionObserver) OnJobEnd(wf.JobEndEvent) {
	if o.done != nil {
		close(o.done)
	}
}
func (o *metricsConsumptionObserver) OnTaskEnd(event wf.TaskEndEvent) {
	if o.entered != nil {
		o.first.Do(func() { close(o.entered); <-o.release })
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.tokens += metricsConsumptionTokens(event.Output)
	o.events = append(o.events, event)
}
func (o *metricsConsumptionObserver) unblock() { o.close.Do(func() { close(o.release) }) }
func (o *metricsConsumptionObserver) total() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.tokens
}
func (o *metricsConsumptionObserver) records() []wf.TaskEndEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]wf.TaskEndEvent(nil), o.events...)
}

func consumedMetricTokens(ctx context.Context) int64 {
	a := taskMetrics(ctx)
	if a == nil {
		return 0
	}
	points := a.Job().Snapshot().Series["fixture_tokens_total"]
	if len(points) == 0 {
		return 0
	}
	return points[0].Value.(int64)
}
