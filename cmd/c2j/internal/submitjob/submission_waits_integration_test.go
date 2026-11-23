package submitjob

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/runjob"
	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/colony-2/jobdb/pkg/jobdb/runtimetest"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type waitFixture struct {
	ctx  context.Context
	db   jobdb.ConsumerRuntime
	wire *remote.Runtime
	opts Options
}

func newWaitFixture(t *testing.T, backend string) *waitFixture {
	t.Helper()
	ctx := celltest.Context(runtimetest.PrimaryGoalContext(context.Background()))
	var db jobdb.ConsumerRuntime = toy.New()
	if backend == "sqlite" {
		rt, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, rt.Close(context.Background())) })
		db = rt
	}
	celltest.Create(t, db, "waits", "app", "example.test/app")
	server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(db)))
	t.Cleanup(server.Close)
	wire, err := remote.New(server.URL, server.Client())
	require.NoError(t, err)
	return &waitFixture{ctx: ctx, db: db, wire: wire, opts: Options{JobDBURI: server.URL + "/waits", WorkingDir: t.TempDir(), Cell: "app", CellRepo: "example.test/app", Prompt: "continue", JSONOutput: true, Stdout: io.Discard, Stderr: io.Discard}}
}

func (f *waitFixture) target(t *testing.T, id, goal string) jobdb.JobHandle {
	t.Helper()
	ref, err := f.db.SubmitJob(f.ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "waits", JobID: id, GoalID: goal, Cell: "app", CellRepo: "example.test/app", JobType: "work", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	return ref
}

func (f *waitFixture) submit(t *testing.T, opts Options) jobdb.JobKey {
	t.Helper()
	var out bytes.Buffer
	opts.Stdout = &out
	require.NoError(t, Run(f.ctx, opts))
	var ref submitResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &ref))
	require.Empty(t, ref.GoalID)
	require.NotEmpty(t, ref.JobID)
	return jobdb.JobKey{TenantId: ref.TenantID, JobId: ref.JobID}
}

func (f *waitFixture) finish(t *testing.T, key jobdb.JobKey, status string) {
	t.Helper()
	lease, err := f.db.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: key, WorkerID: "target-worker", Routes: []jobdb.Route{{JobType: "work"}}})
	require.NoError(t, err)
	require.NotNil(t, lease)
	final := jobdb.Chapter{Ordinal: 1, TaskType: "work", CreatedAt: time.Now().UTC(), Body: jobdb.JobAttemptOutcomeChapter{Outcome: jobdb.ApplicationOutputOutcome{Output: jobdb.ApplicationOutputBytes{Data: []byte(`null`)}}}}
	require.NoError(t, lease.Complete(f.ctx, jobdb.CompleteExecutionRequest{Status: status, Detail: "target " + status, Chapter: &final}))
}

func (f *waitFixture) blocked(t *testing.T, key jobdb.JobKey) {
	t.Helper()
	// Reads, targeted acquisition and polling must all preserve both gates.
	for _, db := range []jobdb.ConsumerRuntime{f.db, f.wire} {
		_, err := db.GetJob(f.ctx, key)
		require.NoError(t, err)
		lease, err := db.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: key, WorkerID: "consumer", Routes: []jobdb.Route{{JobType: "recipe"}}})
		require.NoError(t, err)
		require.True(t, lease == nil, "pending consumer received a lease")
		leases, err := db.PollWork(f.ctx, jobdb.PollWorkRequest{TenantId: "waits", WorkerID: "consumer", Routes: []jobdb.Route{{JobType: "recipe"}}, Limit: 1})
		require.NoError(t, err)
		require.Empty(t, leases, "pending job must reject polling")
	}
}

func TestSubmissionWaitOutcomes(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite"} {
		for _, kind := range []WaitKind{WaitJob, WaitJobSuccess} {
			for _, status := range []string{"succeeded", "failed_app", "failed_system", "failed_timeout", "cancelled"} {
				t.Run(backend+"/"+string(kind)+"/"+status, func(t *testing.T) {
					f := newWaitFixture(t, backend)
					target := f.target(t, "target", "")
					id := target.JobKey.JobId
					if kind == WaitGoal {
						id = target.GoalID
					}
					opts := f.opts
					opts.Waits = []Wait{{kind, id}}
					key := f.submit(t, opts)
					f.blocked(t, key)
					f.finish(t, target.JobKey, status)
					if kind == WaitJob || status == "succeeded" {
						lease, err := f.wire.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: key, WorkerID: "consumer", Routes: []jobdb.Route{{JobType: "recipe"}}})
						require.NoError(t, err)
						require.NotNil(t, lease)
					} else {
						if kind == WaitGoal {
							f.blocked(t, key)
						}
						wait, err := f.db.GetDependencyWait(f.ctx, jobdb.DependencyWaitKey{JobKey: key, WaitID: "submission"})
						require.NoError(t, err)
						if kind == WaitGoal {
							require.Equal(t, jobdb.DependencyPending, wait.State)
						} else {
							require.Equal(t, jobdb.DependencyFailed, wait.State)
							// A settlement lease may record failure, but application code
							// must never be called. Use the runner's admission path.
							called := false
							worker := jobworkflow.ExecutionWorker{JobType: "recipe", Execute: func(jobworkflow.ExecutionContext, jobdb.JobData) (jobdb.JobData, error) {
								called = true
								return jobdb.NewTaskDataOrPanic(nil), nil
							}}
							runnable, err := jobworkflow.GetJobForRun(f.ctx, f.wire, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: worker, WorkerID: "settler", LeaseDuration: time.Minute})
							require.NoError(t, err)
							outcome, err := runnable.Run(nil)
							require.NoError(t, err)
							require.Equal(t, jobworkflow.JobRunFailed, outcome.Status)
							require.False(t, called, "failed success requirement executed recipe code")
						}
					}
				})
			}
		}
	}
}

func TestSubmissionWaitAllOfAndGoalReplacement(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			f := newWaitFixture(t, backend)
			a := f.target(t, "a", "")
			b := f.target(t, "b", "")
			original := f.target(t, "original", "")
			opts := f.opts
			opts.Waits = []Wait{{WaitJob, "a"}, {WaitJobSuccess, "b"}, {WaitJobSuccess, original.JobKey.JobId}}
			key := f.submit(t, opts)
			f.finish(t, a.JobKey, "failed_app")
			f.blocked(t, key)
			f.finish(t, b.JobKey, "succeeded")
			f.blocked(t, key)
			f.finish(t, original.JobKey, "cancelled")
			replacement := f.target(t, "replacement", original.GoalID)
			f.finish(t, replacement.JobKey, "succeeded")
			lease, err := f.wire.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: key, WorkerID: "consumer", Routes: []jobdb.Route{{JobType: "recipe"}}})
			require.NoError(t, err)
			require.NotNil(t, lease)
			wait, err := f.db.GetDependencyWait(f.ctx, jobdb.DependencyWaitKey{JobKey: key, WaitID: "submission"})
			require.NoError(t, err)
			require.Equal(t, jobdb.DependencyFailed, wait.State, "a successful sibling cannot satisfy original's success dependency")
		})
	}
}

func TestSubmissionWaitDependencyReleasePreservesFutureTime(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite"} {
		for _, kind := range []WaitKind{WaitJob, WaitJobSuccess} {
			t.Run(backend+"/"+string(kind), func(t *testing.T) {
				f := newWaitFixture(t, backend)
				target := f.target(t, "target", "")
				id := "target"
				if kind == WaitGoal {
					id = target.GoalID
				}
				opts := f.opts
				opts.Waits = []Wait{{kind, id}}
				opts.WaitUntil = []string{"1h"}
				key := f.submit(t, opts)
				f.blocked(t, key)
				f.finish(t, target.JobKey, "succeeded")
				f.blocked(t, key)
			})
		}
	}
}

func TestSubmissionWaitAlreadySatisfiedAndRejectedTargets(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			f := newWaitFixture(t, backend)
			target := f.target(t, "target", "")
			f.finish(t, target.JobKey, "succeeded")
			opts := f.opts
			opts.Waits = []Wait{{WaitJob, "target"}}
			key := f.submit(t, opts)
			lease, err := f.wire.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: key, WorkerID: "consumer", Routes: []jobdb.Route{{JobType: "recipe"}}})
			require.NoError(t, err)
			require.NotNil(t, lease)
			for _, kind := range []WaitKind{WaitJob, WaitJobSuccess} {
				opts := f.opts
				opts.JobID = "rejected-" + string(kind)
				opts.Waits = []Wait{{kind, "missing"}}
				err := Run(f.ctx, opts)
				require.Error(t, err)
				_, err = f.db.GetJob(f.ctx, jobdb.JobKey{TenantId: "waits", JobId: opts.JobID})
				require.ErrorIs(t, err, jobdb.ErrJobNotFound)
			}
		})
	}
}

func TestSubmissionWaitAnchorBeforePreparation(t *testing.T) {
	// The interactive input finishes before the clock is read. A network delay
	// then consumes the entire delay; no second freshness check is allowed.
	f := newWaitFixture(t, "sqlite")
	handler := remote.NewConsumerServer(f.db)
	var anchor time.Time
	first := true
	var clockRead bool
	server := httptest.NewServer(celltest.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, clockRead, "registry/network work must follow resolution")
		if first {
			first = false
			time.Sleep(100 * time.Millisecond)
		}
		handler.ServeHTTP(w, r)
	})))
	defer server.Close()
	reader := strings.NewReader("interactive prompt\n")
	var stderr bytes.Buffer
	opts := f.opts
	opts.JobDBURI = server.URL + "/waits"
	opts.Prompt = ""
	opts.Stdin = reader
	opts.Stderr = &stderr
	opts.WaitUntil = []string{"50ms"}
	opts.Clock = func() time.Time {
		require.Zero(t, reader.Len())
		require.False(t, clockRead)
		clockRead = true
		anchor = time.Now().UTC()
		return anchor
	}
	key := f.submit(t, opts)
	require.Contains(t, stderr.String(), anchor.Add(50*time.Millisecond).Format(time.RFC3339Nano))
	chapter, err := f.db.GetChapter(f.ctx, jobdb.ChapterRef{JobKey: key, Ordinal: 0})
	require.NoError(t, err)
	raw := chapter.Body.(jobdb.JobStartChapter).Input.Data
	var start workflowctl.StartJob
	require.NoError(t, json.Unmarshal(raw, &start))
	require.Equal(t, anchor.Add(50*time.Millisecond), *start.AvailableAt)
	lease, err := f.db.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: key, WorkerID: "consumer", Routes: []jobdb.Route{{JobType: "recipe"}}})
	require.NoError(t, err)
	require.NotNil(t, lease)
}

func TestInvalidWaitsDoNotPrepareOrPublish(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "unexpected", 500) }))
	defer server.Close()
	for _, mutate := range []func(*Options){
		func(o *Options) { o.Waits = []Wait{{WaitJob, " "}} },
		func(o *Options) { o.WaitUntil = []string{"0s"} },
		func(o *Options) { o.WaitUntil = []string{"1h", "1h"} },
	} {
		opts := Options{JobDBURI: server.URL + "/t", WorkingDir: t.TempDir(), Cell: "app", Prompt: "test", FromWorkspaceNow: true, Stdout: io.Discard, Stderr: io.Discard}
		mutate(&opts)
		require.Error(t, Run(context.Background(), opts))
	}
	require.Zero(t, calls)
}

func TestSubmissionWaitTimeFirstAndConsumerCancellation(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			f := newWaitFixture(t, backend)
			target := f.target(t, "target", "")
			opts := f.opts
			opts.Waits = []Wait{{WaitJob, "target"}}
			opts.WaitUntil = []string{"1h"}
			opts.Clock = func() time.Time { return time.Now().Add(-2 * time.Hour) }
			key := f.submit(t, opts)
			f.blocked(t, key)
			f.finish(t, target.JobKey, "succeeded")
			lease, err := f.wire.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: key, WorkerID: "consumer", Routes: []jobdb.Route{{JobType: "recipe"}}})
			require.NoError(t, err)
			require.NotNil(t, lease)

			cancelledOpts := f.opts
			cancelledOpts.WaitUntil = []string{"1h"}
			cancelled := f.submit(t, cancelledOpts)
			require.NoError(t, f.db.CancelJob(f.ctx, jobdb.CancelJobRequest{JobKey: cancelled, Reason: "stop"}))
			lease, err = f.wire.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: cancelled, WorkerID: "consumer", Routes: []jobdb.Route{{JobType: "recipe"}}})
			require.NoError(t, err)
			require.True(t, lease == nil)
		})
	}
}

func TestSubmissionWaitRunnerTimeoutKeepsPendingJob(t *testing.T) {
	f := newWaitFixture(t, "sqlite")
	target := f.target(t, "target", "")
	opts := f.opts
	opts.Waits = []Wait{{WaitJobSuccess, target.JobKey.JobId}}
	opts.WaitUntil = []string{"1h"}
	key := f.submit(t, opts)
	var logs bytes.Buffer
	err := runjob.Run(f.ctx, runjob.Options{JobDBURI: opts.JobDBURI, JobID: key.JobId, WorkingDir: opts.WorkingDir, WaitTimeout: 20 * time.Millisecond, PollInterval: 5 * time.Millisecond, Stdout: &logs, Stderr: &logs})
	require.ErrorContains(t, err, "timed out waiting")
	f.blocked(t, key)
	goal, err := f.db.GetGoal(f.ctx, jobdb.GoalKey{TenantId: "waits", GoalID: target.GoalID})
	require.NoError(t, err)
	require.Equal(t, jobdb.GoalOpen, goal.State)
}
