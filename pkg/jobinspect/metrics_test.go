package jobinspect_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/consumerfixture"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/metricshistory"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type cancellationWriteRuntime struct {
	jobdb.ConsumerRuntime
	beforeStore func(context.Context) error
	fired       bool
}

// Retain the server's optional schema/configuration/tokenized lease APIs.
type cancellationRemoteRuntime struct {
	consumerfixture.Runtime
	fault *cancellationWriteRuntime
}

func (r cancellationRemoteRuntime) PutChapter(ctx context.Context, q jobdb.PutChapterRequest) error {
	return r.fault.PutChapter(ctx, q)
}

func (r *cancellationWriteRuntime) PutChapter(ctx context.Context, q jobdb.PutChapterRequest) error {
	if body, ok := q.Chapter.Body.(jobdb.ExecutionChapter); ok {
		record, err := jobdb.DecodeExecutionRecord(body.Record)
		if err != nil {
			return err
		}
		if record.Kind == "result" && strings.Contains(record.ID, `/task:"late"#1/`) {
			// Both stores check lease authority before opening uploads. This
			// deterministic pause admits the write, cancels and inspects the job,
			// then lets that already-admitted append become visible. For remote,
			// this wrapper runs on the server, after upload transport decoding.
			q.ArtifactUploads = append(q.ArtifactUploads, jobdb.ArtifactUpload{Name: "in-flight.txt", Size: 0, Open: func() (io.ReadCloser, error) {
				r.fired = true
				if err := r.beforeStore(ctx); err != nil {
					return nil, err
				}
				return io.NopCloser(strings.NewReader("")), nil
			}})
		}
	}
	return r.ConsumerRuntime.PutChapter(ctx, q)
}

func cancellationMetricOutput(value int64) jobdb.TaskData {
	return jobdb.NewTaskDataOrPanic(coretask.OutputEnvelope{Version: coretask.OutputEnvelopeVersion, Kind: coretask.OutputKindInternalResult, Payload: json.RawMessage(`{}`), Metrics: &metrics.Channels{
		Runtime:  metrics.Channel{Status: "absent"},
		Producer: metrics.Channel{Status: "valid", Report: &metrics.Report{Version: 1, Mutations: []metrics.Mutation{{Descriptor: metrics.Descriptor{Name: "tokens", Kind: "counter", NumberType: "int64", Unit: "{token}"}, Op: "add", Value: value}}}},
	}})
}

// F07: cancellation has no frozen stored snapshot. Accepted in-flight history
// can still advance; each inspection states its own observed, computed cutoff.
func TestMetricsLateAcceptedWriteAfterCancellation(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			var base jobdb.ConsumerRuntime = toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))
			if backend != "toy" {
				s, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, s.Close(ctx)) })
				base = s
			}
			celltest.Create(t, base, "tenant", "test", "example.test/test")
			fault := &cancellationWriteRuntime{ConsumerRuntime: base}
			var rt jobdb.ConsumerRuntime = fault
			if backend == "remote" {
				server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(cancellationRemoteRuntime{consumerfixture.New(base.(*sqlite.Runtime)), fault})))
				t.Cleanup(server.Close)
				client, err := remote.New(server.URL, server.Client())
				require.NoError(t, err)
				rt = client
			}
			worker := &fixtureTask{outputs: []jobdb.TaskData{cancellationMetricOutput(3), cancellationMetricOutput(7)}, fail: -1}
			orchestrator := wf.ExecutionWorker{JobType: "cancellation-metrics", Execute: func(c wf.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
				for _, site := range []string{"first", "late"} {
					if _, err := c.DoTask(wf.TaskCall{Site: site, TaskType: worker.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return in, nil }); err != nil {
						return nil, err
					}
				}
				return in, nil
			}}
			handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: orchestrator.Name(), Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
			require.NoError(t, err)
			// Server-side inspection avoids a nested HTTP call while the client
			// awaits its append, but reads the same backend as the public service.
			api := &jobinspect.Service{Runtime: base, Tenant: "tenant"}
			var before *jobinspect.Response
			fault.beforeStore = func(ctx context.Context) error {
				if err := base.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: handle.JobKey, Reason: "fixture cancellation"}); err != nil {
					return err
				}
				var err error
				before, err = api.Inspect(ctx, jobinspect.Request{Operation: "metrics", JobID: handle.JobKey.JobId, Metrics: &metrics.Query{Deltas: true}})
				return err
			}
			run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: orchestrator, TaskWorkers: []wf.TaskWorker{worker}, WorkerID: "cancellation", LeaseDuration: time.Minute})
			require.NoError(t, err)
			_, _ = run.Run(nil) // cancellation fences finalization after the append
			require.True(t, fault.fired, "must reach the actual post-admission storage boundary")
			require.NotNil(t, before)
			require.Equal(t, "computed", before.Metrics.Snapshot.Source)
			require.Equal(t, int64(3), before.Metrics.Snapshot.Series["tokens"][0].Value)
			for _, report := range before.Metrics.Reports {
				require.NotContains(t, report.Task, `/task:"late"#1`, "uncommitted usage is not a measured zero")
			}
			publicAPI := &jobinspect.Service{Runtime: rt, Tenant: "tenant"}
			after, err := publicAPI.Inspect(ctx, jobinspect.Request{Operation: "metrics", JobID: handle.JobKey.JobId, Metrics: &metrics.Query{Deltas: true}})
			require.NoError(t, err)
			require.Equal(t, "computed", after.Metrics.Snapshot.Source)
			require.Equal(t, "complete", after.Metrics.Snapshot.Coverage.History)
			require.Equal(t, int64(10), after.Metrics.Snapshot.Series["tokens"][0].Value)
			require.Greater(t, after.Metrics.Snapshot.Cutoff, before.Metrics.Snapshot.Cutoff)
			require.Zero(t, after.Metrics.Snapshot.Coverage.Reports["producer"]["pending"])
			lateReports := 0
			for _, report := range after.Metrics.Reports {
				if report.Channel == "producer" && strings.Contains(report.Task, `/task:"late"#1`) {
					lateReports++
					require.Equal(t, int64(7), report.ChannelReport.Report.Mutations[0].Value)
					require.Greater(t, report.Ordinal, before.Metrics.Snapshot.Cutoff)
				}
			}
			require.Equal(t, 1, lateReports)
			pinned, err := publicAPI.Inspect(ctx, jobinspect.Request{Operation: "metrics", JobID: handle.JobKey.JobId, Metrics: &metrics.Query{ThroughOrdinal: &before.Metrics.Snapshot.Cutoff, Deltas: true}})
			require.NoError(t, err)
			require.Equal(t, before.Metrics, pinned.Metrics, "old observed cutoff remains reproducible")
			info, err := rt.GetJob(ctx, handle.JobKey)
			require.NoError(t, err)
			require.Equal(t, jobdb.JobStatusCancelled, info.Status)
			chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
			require.NoError(t, err)
			for _, chapter := range chapters {
				snapshot, err := metricshistory.Stored(chapter)
				require.NoError(t, err)
				require.Nil(t, snapshot, "computed cancellation totals must not claim a stored final snapshot")
			}
		})
	}
}
