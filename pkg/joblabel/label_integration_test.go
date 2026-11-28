//go:build integration

package joblabel_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/joblabel"
	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtimetest"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T) (context.Context, *sqlite.Runtime, jobdb.JobKey) {
	t.Helper()
	ctx := celltest.Context(context.Background())
	runtime, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), Projects: celltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(ctx)) })
	job, err := celltest.Submit(runtime, ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{
		TenantId: "labeltest", JobID: "job", Cell: "test", CellRepo: "example.test/test", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(nil),
	}})
	require.NoError(t, err)
	return ctx, runtime, job.JobKey
}

func assertReceipt(t *testing.T, state joblabel.State, err error, status joblabel.Status, version joblabel.Version) {
	t.Helper()
	require.NoError(t, err)
	require.Equal(t, status, state.Status)
	require.NotNil(t, state.Version)
	require.Equal(t, version, *state.Version)
}

func TestJobLabelsGoRoundTrip(t *testing.T) {
	for _, surface := range []string{"native", "tenant-http", "project-http"} {
		t.Run(surface, func(t *testing.T) {
			ctx, db, key := fixture(t)
			var runtime jobdb.ConsumerRuntime = db
			if surface != "native" {
				family := "tenants"
				if surface == "project-http" {
					family = "projects"
				}
				handler := remote.NewConsumerServer(db)
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					if !strings.HasPrefix(request.URL.Path, "/v1/"+family+"/"+key.TenantId+"/") {
						t.Errorf("unexpected route: %s", request.URL.Path)
						http.NotFound(writer, request)
						return
					}
					handler.ServeHTTP(writer, request.WithContext(celltest.Context(request.Context())))
				}))
				t.Cleanup(server.Close)
				var err error
				if surface == "project-http" {
					runtime, err = remote.NewProjectRuntime(server.URL, server.Client())
				} else {
					runtime, err = remote.New(server.URL, server.Client())
				}
				require.NoError(t, err)
			}
			state, err := joblabel.RemoveJobLabel(ctx, runtime, key, "missing", 0)
			assertReceipt(t, state, err, joblabel.Absent, 0)
			state, err = joblabel.SetJobLabel(ctx, runtime, key, "friend", " foe ☃ ", joblabel.Blue, 0)
			assertReceipt(t, state, err, joblabel.Present, 1)
			require.Equal(t, " foe ☃ ", state.Label.Value)
			state, err = joblabel.SetJobLabel(ctx, runtime, key, "friend", " foe ☃ ", joblabel.Blue, 1)
			assertReceipt(t, state, err, joblabel.Present, 1)
			updated := joblabel.Label{Key: "friend", Value: " updated ", Color: joblabel.Orange, Version: 2}
			t.Run("update-present", func(t *testing.T) {
				state, err := joblabel.SetJobLabel(ctx, runtime, key, updated.Key, updated.Value, updated.Color, 1)
				assertReceipt(t, state, err, joblabel.Present, 2)
				require.Equal(t, &updated, state.Label)
				read, err := joblabel.GetJobLabel(ctx, runtime, key, updated.Key)
				require.NoError(t, err)
				require.Equal(t, state, read)
			})
			state, err = joblabel.SetJobLabel(ctx, runtime, key, "alpha", "a", joblabel.Gray, 0)
			assertReceipt(t, state, err, joblabel.Present, 1)
			state, err = joblabel.GetJobLabel(ctx, runtime, key, "missing")
			assertReceipt(t, state, err, joblabel.Absent, 0)
			view, err := joblabel.ListJobLabels(ctx, runtime, key)
			require.NoError(t, err)
			require.Equal(t, joblabel.Projected, view.Projection)
			require.Equal(t, []joblabel.Label{{Key: "alpha", Value: "a", Color: joblabel.Gray, Version: 1}, updated}, view.Labels)
			list, err := runtime.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{key.TenantId}})
			require.NoError(t, err)
			require.Len(t, list.Jobs, 1)
			require.Equal(t, view.Labels, joblist.JobFromSummary(list.Jobs[0]).Labels)
			state, err = joblabel.RemoveJobLabel(ctx, runtime, key, "friend", 2)
			assertReceipt(t, state, err, joblabel.Tombstone, 3)
			state, err = joblabel.RemoveJobLabel(ctx, runtime, key, "friend", 3)
			assertReceipt(t, state, err, joblabel.Tombstone, 3)
			state, err = joblabel.GetJobLabel(ctx, runtime, key, "friend")
			assertReceipt(t, state, err, joblabel.Tombstone, 3)
			state, err = joblabel.SetJobLabel(ctx, runtime, key, "friend", "recreated", joblabel.Green, 3)
			assertReceipt(t, state, err, joblabel.Present, 4)
		})
	}
}

func TestJobLabelsMalformedRawMetadata(t *testing.T) {
	ctx, runtime, key := fixture(t)
	for name, raw := range map[string]string{
		"c2j.label.bad":     `{"value":"private-malformed","color":"navy"}`,
		"c2j.label.remove":  `{"value":null,"color":"gray"}`,
		"c2j.label.Fault":   `{"value":"raw invalid suffix","color":"blue"}`,
		"c2j.label.extra":   `{"value":"x","color":"red","other":false}`,
		"c2j.label.line":    `{"value":"x\u2028y","color":"red"}`,
		"c2j.label.missing": `{"value":"x"}`,
		"c2j.label.blank":   `{"value":" 　","color":"red"}`,
		"unrelated":         `{"value":"not a label","color":"gray"}`,
	} {
		_, err := runtime.EditJobMetadata(ctx, key, jobdb.JobMetadataEdit{Key: name, ExpectedVersion: new(joblabel.Version), Set: json.RawMessage(raw)})
		require.NoError(t, err)
	}
	state, err := joblabel.SetJobLabel(ctx, runtime, key, "gone", "x", joblabel.Blue, 0)
	assertReceipt(t, state, err, joblabel.Present, 1)
	state, err = joblabel.RemoveJobLabel(ctx, runtime, key, "gone", 1)
	assertReceipt(t, state, err, joblabel.Tombstone, 2)
	before, err := runtime.GetJob(ctx, key)
	require.NoError(t, err)
	state, err = joblabel.GetJobLabel(ctx, runtime, key, "bad")
	assertReceipt(t, state, err, joblabel.Malformed, 1)
	require.Nil(t, state.Label)
	_, err = joblabel.GetJobLabel(ctx, runtime, key, "Fault")
	require.ErrorIs(t, err, joblabel.ErrInvalidKey)
	view, err := joblabel.ListJobLabels(ctx, runtime, key)
	require.NoError(t, err)
	require.Empty(t, view.Labels)
	for _, raw := range []string{`{"value":"x","value":"x","color":"blue"}`, `{"value":"\ud800","color":"blue"}`, "{\"value\":\"\xff\",\"color\":\"blue\"}"} {
		view := joblabel.FromProjection(map[string]jobdb.JobMetadataEntry{"c2j.label.bad": {Present: true, Version: 1, Value: json.RawMessage(raw)}})
		require.Empty(t, view.Labels)
	}
	after, err := runtime.GetJob(ctx, key)
	require.NoError(t, err)
	require.Equal(t, before.ExtendedMetadata, after.ExtendedMetadata)
	state, err = joblabel.SetJobLabel(ctx, runtime, key, "bad", "safe", joblabel.Green, 1)
	assertReceipt(t, state, err, joblabel.Present, 2)
	state, err = joblabel.RemoveJobLabel(ctx, runtime, key, "remove", 1)
	assertReceipt(t, state, err, joblabel.Tombstone, 2)
	version := joblabel.Version(1)
	removed, err := runtime.EditJobMetadata(ctx, key, jobdb.JobMetadataEdit{Key: "c2j.label.Fault", ExpectedVersion: &version, Remove: true})
	require.NoError(t, err)
	require.Equal(t, jobdb.JobMetadataEntry{Version: 2}, removed)
}

type consumerOnly struct{ jobdb.ConsumerRuntime }
type editFailure struct {
	jobdb.ConsumerRuntime
	fail  error
	calls atomic.Int32
}

func (runtime *editFailure) EditJobMetadata(context.Context, jobdb.JobKey, jobdb.JobMetadataEdit) (jobdb.JobMetadataEntry, error) {
	runtime.calls.Add(1)
	return jobdb.JobMetadataEntry{}, runtime.fail
}

func TestJobLabelsUnsupportedAndDenied(t *testing.T) {
	ctx, db, key := fixture(t)
	t.Run("missing-port", func(t *testing.T) {
		_, err := joblabel.RemoveJobLabel(ctx, consumerOnly{db}, key, "label", 0)
		require.ErrorIs(t, err, jobdb.ErrJobMetadataUnsupported)
	})
	for _, entry := range []struct {
		name   string
		status int
		err    error
	}{
		{"explicit-unsupported", http.StatusNotImplemented, jobdb.ErrJobMetadataUnsupported},
		{"old-route", http.StatusNotFound, jobdb.ErrJobNotFound},
		{"denied-write", http.StatusForbidden, jobdb.ErrLeasePermissionDenied},
	} {
		t.Run(entry.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(entry.status) }))
			defer server.Close()
			client, err := remote.New(server.URL, server.Client())
			require.NoError(t, err)
			_, err = joblabel.RemoveJobLabel(ctx, client, key, "label", 0)
			require.ErrorIs(t, err, entry.err)
		})
	}
}

func TestJobLabelsStaleEdits(t *testing.T) {
	ctx, runtime, key := fixture(t)
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, value := range []string{"one", "two"} {
		go func() {
			<-start
			_, err := joblabel.SetJobLabel(ctx, runtime, key, "same", value, joblabel.Blue, 0)
			results <- err
		}()
	}
	close(start)
	require.ElementsMatch(t, []error{nil, jobdb.ErrConflict}, []error{<-results, <-results})
	state, err := joblabel.SetJobLabel(ctx, runtime, key, "other", "one", joblabel.Red, 0)
	assertReceipt(t, state, err, joblabel.Present, 1)
	_, err = joblabel.SetJobLabel(ctx, runtime, key, "same", "stale", joblabel.Red, 0)
	require.ErrorIs(t, err, jobdb.ErrConflict)
	_, err = joblabel.RemoveJobLabel(ctx, runtime, key, "same", 0)
	require.ErrorIs(t, err, jobdb.ErrConflict)
	state, err = joblabel.RemoveJobLabel(ctx, runtime, key, "same", 1)
	assertReceipt(t, state, err, joblabel.Tombstone, 2)
	_, err = joblabel.SetJobLabel(ctx, runtime, key, "same", "stale", joblabel.Red, 1)
	require.ErrorIs(t, err, jobdb.ErrConflict)
	transport := &editFailure{ConsumerRuntime: runtime, fail: errors.New("transport dropped receipt")}
	state, err = joblabel.RemoveJobLabel(ctx, transport, key, "same", 2)
	require.ErrorIs(t, err, transport.fail)
	require.Zero(t, state)
	require.EqualValues(t, 1, transport.calls.Load())
}

func TestJobLabelsTerminalAndRetained(t *testing.T) {
	ctx, runtime, key := fixture(t)
	state, err := joblabel.SetJobLabel(ctx, runtime, key, "before", "live", joblabel.Green, 0)
	assertReceipt(t, state, err, joblabel.Present, 1)
	lease, err := runtime.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: key, Routes: []jobdb.Route{{JobType: "recipe"}}, WorkerID: "worker", LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.NotNil(t, lease)
	failure := jobdb.AppErrorOutcome{Error: jobdb.AppErrorPayload{Message: "failed"}}
	require.NoError(t, lease.Complete(ctx, jobdb.CompleteExecutionRequest{Status: "failed", Chapter: &jobdb.Chapter{Ordinal: 1, TaskType: "recipe", CreatedAt: time.Now(), Body: jobdb.JobAttemptOutcomeChapter{Outcome: failure}}}))
	state, err = joblabel.SetJobLabel(ctx, runtime, key, "after", "retained", joblabel.Blue, 0)
	assertReceipt(t, state, err, joblabel.Present, 1)
	view, err := joblabel.ListJobLabels(ctx, runtime, key)
	require.NoError(t, err)
	require.Len(t, view.Labels, 2)
	archived, err := runtime.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{key.TenantId}, Stores: []jobdb.JobStore{jobdb.JobStoreArchived}})
	require.NoError(t, err)
	require.Len(t, archived.Jobs, 1)
	require.Equal(t, view.Labels, joblist.JobFromSummary(archived.Jobs[0]).Labels)
	t.Run("synthetic-terminal", func(t *testing.T) {
		syntheticCtx := runtimetest.PrimaryGoalContext(ctx)
		job, err := celltest.Submit(runtime, syntheticCtx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{
			TenantId: key.TenantId, JobID: "synthetic-source", Cell: "test", CellRepo: "example.test/test", JobType: "work", Data: jobdb.NewTaskDataOrPanic(nil),
		}})
		require.NoError(t, err)
		goal, err := runtime.FailGoal(syntheticCtx, jobdb.FailGoalRequest{GoalKey: jobdb.GoalKey{TenantId: key.TenantId, GoalID: job.GoalID}, Message: "synthetic test record"})
		require.NoError(t, err)
		synthetic := jobdb.JobKey{TenantId: key.TenantId, JobId: goal.FailureJobID}
		info, err := runtime.GetJob(syntheticCtx, synthetic)
		require.NoError(t, err)
		require.True(t, info.RecordKind.IsTerminalRecord())
		_, err = joblabel.SetJobLabel(syntheticCtx, runtime, synthetic, "label", "foe", joblabel.Blue, 0)
		require.ErrorIs(t, err, jobdb.ErrJobNotFound)
	})
	_, err = joblabel.RemoveJobLabel(ctx, runtime, jobdb.JobKey{TenantId: key.TenantId, JobId: "absent"}, "before", 1)
	require.ErrorIs(t, err, jobdb.ErrJobNotFound)
	deleted, err := runtime.DeleteJobs(ctx, jobdb.DeleteJobsRequest{TenantID: key.TenantId, JobIDs: []string{key.JobId}})
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted.DeletedCount)
	hidden, err := runtime.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{key.TenantId}, JobKeys: []jobdb.JobKey{key}, IncludeHidden: true})
	require.NoError(t, err)
	require.Len(t, hidden.Jobs, 1)
	_, err = joblabel.RemoveJobLabel(ctx, runtime, key, "before", 1)
	require.ErrorIs(t, err, jobdb.ErrJobNotFound)
}
