package executionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/metricshistory"
	"github.com/colony-2/c2j/pkg/metricsruntime"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type usageTask struct {
	name   string
	tokens int64
	fail   bool
	runs   *int
}

func (w usageTask) Name() string { return w.name }
func (w usageTask) Run(tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func (w usageTask) RunTask(local context.Context, tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	*w.runs++
	metricsruntime.Execution(local)
	metrics.FromContext(local).AddInt("llm_tokens_total", "{token}", map[string]string{"provider": "fixture", "model": "returned-model", "token_type": "input"}, w.tokens)
	if w.fail {
		return nil, fmt.Errorf("answer validation failed")
	}
	raw, e := in.GetData()
	if e != nil {
		return nil, e
	}
	var req workerops.ActivityInvocationRequest
	if e = json.Unmarshal(raw, &req); e != nil {
		return nil, e
	}
	env, e := task.NewOutputEnvelope(task.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{OpOutput: map[string]any{"answer": "fixture", "metrics": "business value"}, GitResult: contextual.GitCommitContext{ParentRef: req.GitTaskContext.BaseRef}})
	if e != nil {
		return nil, e
	}
	return jobdb.NewTaskData(env)
}
func TestMetricsCompletionAcrossPinnedBackends(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", backend, failed), func(t *testing.T) {
				f := newFixture(t, backend, "2Gi", nil)
				runs := 0
				f.tasks = []wf.TaskWorker{
					metricsruntime.WrapWorker(usageTask{"execution_discover:work", 9007199254740993, false, &runs}, task.OutputKindActivityInvocationOutput),
					metricsruntime.WrapWorker(usageTask{"execution_finish:work", 2, failed, &runs}, task.OutputKindActivityInvocationOutput),
				}
				outcome, _ := f.run(t, "2Gi", f.tasks)
				if failed {
					require.Equal(t, wf.JobRunFailed, outcome.Status)
				} else {
					require.Equal(t, wf.JobRunCompleted, outcome.Status)
				}
				require.Equal(t, 2, runs)
				ctx := celltest.Context(context.Background())
				chapters, e := f.runtime.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: f.key})
				require.NoError(t, e)
				final := chapters[len(chapters)-1]
				snapshot, e := metricshistory.Stored(final)
				require.NoError(t, e)
				require.NotNil(t, snapshot, "required stored snapshot missing")
				require.Equal(t, "complete", snapshot.Coverage.History)
				require.Equal(t, "final_chapter", snapshot.Source)
				require.Equal(t, int64(9007199254740995), snapshot.Series["llm_tokens_total"][0].Value)
				if !failed {
					var output struct {
						Metrics metrics.Snapshot `json:"c2j_metrics"`
					}
					require.NoError(t, json.Unmarshal(outcome.Output.GetDataOrPanic(), &output))
					require.Equal(t, snapshot.Series, output.Metrics.Series)
					require.Equal(t, snapshot.Coverage, output.Metrics.Coverage)
					body := final.Body.(jobdb.JobAttemptOutcomeChapter).Outcome.(jobdb.ApplicationOutputOutcome)
					require.JSONEq(t, string(outcome.Output.GetDataOrPanic()), string(body.Output.Data))
				}
				inspection := &jobinspect.Service{Runtime: f.runtime, Tenant: f.key.TenantId}
				result, e := inspection.Inspect(ctx, jobinspect.Request{Operation: "metrics", JobID: f.key.JobId, Metrics: &metrics.Query{ThroughOrdinal: &snapshot.Cutoff, Deltas: true}})
				require.NoError(t, e)
				require.Equal(t, snapshot.Series, result.Metrics.Snapshot.Series)
				require.Equal(t, snapshot.Coverage, result.Metrics.Snapshot.Coverage)
				var lastTask string
				for _, r := range result.Metrics.Reports {
					if r.Channel == "producer" && r.ChannelReport.Status == "valid" {
						require.Equal(t, "valid", r.ChannelReport.Status)
						require.Equal(t, "returned-model", r.ChannelReport.Report.Mutations[0].Labels["model"])
						lastTask = r.Task
					}
				}
				log, e := inspection.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.key.JobId, Task: lastTask})
				require.NoError(t, e)
				require.NotNil(t, log.Task.Metrics)
				if failed {
					require.Equal(t, "not_produced", log.Task.OutputAvailability)
				}
				if !failed {
					field := "/metrics"
					logged, e := inspection.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.key.JobId, Task: lastTask, Field: &field})
					require.NoError(t, e)
					require.JSONEq(t, `"business value"`, string(logged.Value))
				}
				// Replaying final completion must keep the winning snapshot bytes and cutoff.
				req := jobdb.CompleteExecutionRequest{Chapter: &final}
				adapter := &lease{ExecutionLease: completionIdentity{key: f.key}, runtime: New(f.runtime, allocation("2Gi")), leaseState: &leaseState{}}
				reused := adapter.finalizeMetrics(ctx, req)
				require.Equal(t, final.Metadata.Fields["metadata"].Map[metricshistory.SnapshotMetadataKey], reused.Chapter.Metadata.Fields["metadata"].Map[metricshistory.SnapshotMetadataKey])
			})
		}
	}
}

type completionIdentity struct {
	jobdb.ExecutionLease
	key jobdb.JobKey
}

func (l completionIdentity) Job() jobdb.JobHandle { return jobdb.JobHandle{JobKey: l.key} }
func (l completionIdentity) Route() jobdb.Route   { return jobdb.Route{JobType: "recipe"} }

type failingMetricRead struct{ jobdb.WorkflowRuntime }

func (failingMetricRead) ListChapters(context.Context, jobdb.ListChaptersRequest) ([]jobdb.Chapter, error) {
	return nil, errors.New("read failed")
}
func TestUnavailableCompletionAndCachedCutoff(t *testing.T) {
	f := newFixture(t, "toy", "2Gi", nil)
	ctx := celltest.Context(context.Background())
	adapter := &lease{ExecutionLease: completionIdentity{key: f.key}, runtime: New(failingMetricRead{f.runtime}, allocation("2Gi")), leaseState: &leaseState{}}
	ch := jobdb.Chapter{Ordinal: 5, Metadata: jobdb.ChapterMetadata{Fields: map[string]jobdb.ChapterMetadataValue{"kept": {Kind: jobdb.ChapterMetadataString, String: "original"}}}}
	req := jobdb.CompleteExecutionRequest{Status: "FAILED", Detail: "original failure", Chapter: &ch}
	first := adapter.finalizeMetrics(ctx, req)
	snapshot, e := metricshistory.Stored(*first.Chapter)
	require.NoError(t, e)
	require.Equal(t, "unavailable", snapshot.Coverage.History)
	require.Empty(t, snapshot.Series)
	require.Equal(t, "FAILED", first.Status)
	require.Equal(t, "original failure", first.Detail)
	require.Len(t, ch.Metadata.Fields, 1)
	adapter.runtime.WorkflowRuntime = f.runtime
	second := adapter.finalizeMetrics(ctx, req)
	require.Equal(t, first.Chapter.Metadata.Fields["metadata"].Map[metricshistory.SnapshotMetadataKey], second.Chapter.Metadata.Fields["metadata"].Map[metricshistory.SnapshotMetadataKey])
}

// Commit either the full completion or just its chapter, then lose the response.
// The latter leaves durable output for a fresh runner to reconcile after expiry.
type metricCompletionFault struct {
	jobdb.WorkflowRuntime
	partial bool
	fired   atomic.Bool
}
type metricCompletionFaultLease struct {
	jobdb.ExecutionLease
	fault *metricCompletionFault
}

func (r *metricCompletionFault) CreateDelegation(ctx context.Context, parent jobdb.ExecutionLease, q jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
	if wrapped, ok := parent.(*metricCompletionFaultLease); ok {
		parent = wrapped.ExecutionLease
	}
	return r.WorkflowRuntime.(jobdb.LeaseDelegator).CreateDelegation(ctx, parent, q)
}

func (r *metricCompletionFault) GetJobLease(ctx context.Context, q jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	l, err := r.WorkflowRuntime.GetJobLease(ctx, q)
	if err != nil || l == nil {
		return l, err
	}
	return &metricCompletionFaultLease{l, r}, nil
}
func (l *metricCompletionFaultLease) LeaseToken() string {
	if token, ok := l.ExecutionLease.(interface{ LeaseToken() string }); ok {
		return token.LeaseToken()
	}
	return ""
}
func (l *metricCompletionFaultLease) Complete(ctx context.Context, q jobdb.CompleteExecutionRequest) error {
	if !l.fault.fired.CompareAndSwap(false, true) {
		return l.ExecutionLease.Complete(ctx, q)
	}
	// Valid but noncanonical JSON detects accidental recomputation during recovery.
	meta := q.Chapter.Metadata.Fields["metadata"]
	value := meta.Map[metricshistory.SnapshotMetadataKey]
	value.String = " \n" + value.String + "\n"
	meta.Map[metricshistory.SnapshotMetadataKey] = value
	if l.fault.partial {
		if err := l.fault.WorkflowRuntime.PutChapter(ctx, jobdb.PutChapterRequest{LeaseID: l.LeaseID(), LeaseToken: l.LeaseToken(), Ref: jobdb.ChapterRef{JobKey: l.Job().JobKey, Ordinal: q.Chapter.Ordinal}, Chapter: *q.Chapter, ArtifactUploads: q.ArtifactUploads}); err != nil {
			return err
		}
	} else if err := l.ExecutionLease.Complete(ctx, q); err != nil {
		return err
	}
	return errors.New("injected lost metric completion response")
}

func TestMetricsUncertainCompletionRecovery(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/chapter-only=%t", backend, partial), func(t *testing.T) {
				f := newFixture(t, backend, "2Gi", nil)
				base := f.runtime
				fault := &metricCompletionFault{WorkflowRuntime: base, partial: partial}
				runs := 0
				tasks := []wf.TaskWorker{
					metricsruntime.WrapWorker(usageTask{"execution_discover:work", 3, false, &runs}, task.OutputKindActivityInvocationOutput),
					metricsruntime.WrapWorker(usageTask{"execution_finish:work", 7, false, &runs}, task.OutputKindActivityInvocationOutput),
				}
				ctx := celltest.Context(context.Background())
				run := func(storage jobdb.WorkflowRuntime) (wf.JobRunOutcome, error) {
					guard := New(storage, allocation("2Gi"))
					worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{Allocation: allocation("2Gi"), StageExecution: guard.Stage, WrapTaskWorker: guard.WrapTaskWorker})
					// Leave time for SQLite/Git setup under repository-wide load.
					// Lease expiry is only needed after the injected chapter-only
					// completion; it must not race the work being tested.
					h, err := wf.GetJobForRun(ctx, guard, wf.GetJobForRunRequest{JobKey: f.key, JobWorker: worker, TaskWorkers: append(guardTasks(guard, tasks), compiler.NewCellContextTaskWorker()), WorkerID: "uncertain-metrics", LeaseDuration: 5 * time.Second})
					if err != nil {
						return wf.JobRunOutcome{}, err
					}
					return h.Run(nil)
				}
				out, err := run(fault)
				if partial {
					require.ErrorContains(t, err, "injected lost metric completion")
				} else {
					require.NoError(t, err)
					require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
				}
				require.True(t, fault.fired.Load())
				before, err := base.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: f.key})
				require.NoError(t, err)
				final := before[len(before)-1]
				winning := final.Metadata.Fields["metadata"].Map[metricshistory.SnapshotMetadataKey].String
				require.Contains(t, winning, " \n")
				snapshot, err := metricshistory.Stored(final)
				require.NoError(t, err)
				require.NotNil(t, snapshot)
				require.Equal(t, final.Ordinal-1, snapshot.Cutoff)
				require.Contains(t, snapshot.Series, "llm_tokens_total", "%v", out.JobError)
				require.Equal(t, int64(10), snapshot.Series["llm_tokens_total"][0].Value)
				reports, err := metricshistory.Read(ctx, base, f.key, metrics.Query{Deltas: true})
				require.NoError(t, err)
				if partial {
					require.Eventually(t, func() bool {
						out, err = run(base) // fresh c2j adapter: its in-memory cache cannot help
						require.NoError(t, err)
						return out.Status != wf.JobRunNotLeaseable
					}, 15*time.Second, 50*time.Millisecond)
					require.Equal(t, wf.JobRunCompleted, out.Status)
				}
				info, err := base.GetJob(ctx, f.key)
				require.NoError(t, err)
				require.Equal(t, jobdb.JobStatusCompleted, info.Status)
				after, err := base.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: f.key})
				require.NoError(t, err)
				require.Equal(t, before, after, "recovery must reuse the winning chapter, including exact snapshot bytes")
				require.Equal(t, winning, after[len(after)-1].Metadata.Fields["metadata"].Map[metricshistory.SnapshotMetadataKey].String)
				afterReports, err := metricshistory.Read(ctx, base, f.key, metrics.Query{Deltas: true})
				require.NoError(t, err)
				require.Equal(t, reports, afterReports, "physical report identities and CPU/timing deltas remain unchanged")
				require.Equal(t, 2, runs, "recovery cannot rerun metric-producing bodies")
			})
		}
	}
}
