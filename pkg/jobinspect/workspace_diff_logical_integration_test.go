package jobinspect_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/c2j/pkg/jobinspect/workspacedifftest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceDiffNativeO02LogicalCutoff(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := savedBackend(t, backend)
			f, err := workspacedifftest.Build(ctx, rt, t.TempDir())
			require.NoError(t, err)
			run, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: f.Job, IncludeInputs: true, IncludeOutputs: true, IncludeArtifacts: true, IncludeAttemptInputs: true})
			require.NoError(t, err)
			api := &jobinspect.Service{Runtime: rt, Tenant: f.Job.TenantId}
			log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
			require.NoError(t, err)
			id := log.Tasks[2].ID
			start, end := -1, -1
			for i, e := range run.Execution {
				if e.Record.ID == id+"/attempt:1" {
					if e.Record.Kind == "attempt" {
						start = i
					}
					if e.Record.Kind == "result" {
						end = i
					}
				}
			}
			require.GreaterOrEqual(t, start, 0)
			require.Greater(t, end, start)
			require.Equal(t, run.Execution[start].Ordinal, run.Execution[end].Ordinal, "native checkpoint reconstructs both logical boundaries in one physical chapter")
			before, err := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, err)
			for _, reader := range []jobinspect.WorkspaceDiffAPI{api, savedHelper(t, ctx, api, f.Job.JobId), savedCLIReader(t, ctx, api, f.Job.JobId)} {
				var first *jobinspect.WorkspaceDiff
				for _, boundary := range []string{"before", "after"} {
					m, body, err := reader.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: &id, Boundary: &boundary}})
					require.NoError(t, err)
					require.Equal(t, "available", m.Status)
					data, err := io.ReadAll(body)
					require.NoError(t, err)
					require.NoError(t, body.Close())
					index := 1
					if boundary == "after" {
						index = 2
					}
					require.Equal(t, log.Tasks[index].ID, m.Source.Producer.Task)
					require.Equal(t, f.Steps[index].Base, data)
					if first != nil {
						require.Equal(t, first.Observation, m.Observation)
						require.NotEqual(t, first.Cutoff, m.Cutoff)
						require.NotEqual(t, first.Workspace.HeadCommit, m.Workspace.HeadCommit)
					}
					first = m
				}
			}
			after, err := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
}

func TestWorkspaceDiffNativeS11S14TaskAttempts(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := savedBackend(t, backend)
			index := 2
			f, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{RetryTask: &index})
			require.NoError(t, err)
			api := &jobinspect.Service{Runtime: rt, Tenant: f.Job.TenantId}
			log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
			require.NoError(t, err)
			task := log.Tasks[index]
			require.Equal(t, []int{1, 2}, task.Attempts)
			before, err := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, err)
			for _, reader := range []jobinspect.WorkspaceDiffAPI{api, savedHelper(t, ctx, api, f.Job.JobId), savedCLIReader(t, ctx, api, f.Job.JobId)} {
				var original *jobinspect.WorkspaceDiff
				for _, attempt := range []int{1, 2, 0, 1} {
					q := jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: &task.ID}}
					if attempt != 0 {
						q.Attempt = &attempt
					}
					m, body, err := reader.OpenWorkspaceDiff(ctx, q)
					require.NoError(t, err)
					require.Equal(t, "available", m.Status, "%v", m.Reasons)
					data, err := io.ReadAll(body)
					require.NoError(t, err)
					require.NoError(t, body.Close())
					require.Equal(t, f.Steps[index].Base, data)
					n := attempt
					if n == 0 {
						n = 2
					}
					require.Equal(t, n, m.Resolved.Attempt)
					require.Equal(t, n, m.StateProducer.Attempt)
					require.Equal(t, n, m.Source.Producer.Attempt)
					if n == 1 {
						require.Equal(t, "diagnostic", m.Resolved.Status)
						if original != nil {
							require.Equal(t, original, m)
						}
						original = m
					} else {
						require.Equal(t, "succeeded", m.Resolved.Status)
						require.NotEqual(t, original.Source.Key, m.Source.Key)
					}
				}
			}
			after, err := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
}

func TestWorkspaceDiffNativeS07ConflictingEmptyEvidence(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := savedBackend(t, backend)
			f, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{ConflictingIncoming: true, Transform: func(steps []workspacedifftest.Step) []workspacedifftest.Step {
				// Negative evidence: contradictory exact incoming keys must never become
				// a successful metadata-only empty comparison.
				steps[3].Before.HeadCommit = ""
				steps[3].After.HeadCommit = ""
				return steps
			}})
			require.NoError(t, err)
			api := &jobinspect.Service{Runtime: rt, Tenant: f.Job.TenantId}
			for _, reader := range []jobinspect.WorkspaceDiffAPI{api, savedHelper(t, ctx, api, f.Job.JobId), savedCLIReader(t, ctx, api, f.Job.JobId)} {
				m, body, err := reader.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId})
				require.NoError(t, err)
				require.Equal(t, "unavailable", m.Status)
				require.Contains(t, m.Reasons[0], "conflicting incoming")
				require.Nil(t, body)
				require.Nil(t, m.Bytes)
				require.Nil(t, m.Source)
			}
		})
	}
}

type corruptWorkspaceProtocol struct {
	jobinspect.Runtime
	invalid bool
}

func (r *corruptWorkspaceProtocol) ListChapters(ctx context.Context, q jobdb.ListChaptersRequest) ([]jobdb.Chapter, error) {
	chapters, err := r.Runtime.ListChapters(ctx, q)
	if err != nil {
		return nil, err
	}
	for i, c := range chapters {
		if body, ok := c.Body.(jobdb.ExecutionChapter); ok {
			if r.invalid {
				body.Record = []byte(`{"version":`)
			} else {
				var record map[string]any
				if err := json.Unmarshal(body.Record, &record); err != nil {
					return nil, err
				}
				record["version"] = 99
				body.Record, err = json.Marshal(record)
				if err != nil {
					return nil, err
				}
			}
			chapters[i].Body = body
			return chapters, nil
		}
	}
	return nil, errors.New("native fixture has no public execution chapter to corrupt")
}

func TestWorkspaceDiffNativeO03UnsupportedProtocol(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := savedBackend(t, backend)
			f, err := workspacedifftest.Build(ctx, rt, t.TempDir())
			require.NoError(t, err)
			for _, invalid := range []bool{false, true} {
				service := &jobinspect.Service{Runtime: &corruptWorkspaceProtocol{Runtime: rt, invalid: invalid}, Tenant: f.Job.TenantId}
				for _, reader := range []jobinspect.WorkspaceDiffAPI{service, savedHelper(t, ctx, service, f.Job.JobId), savedCLIReader(t, ctx, service, f.Job.JobId)} {
					m, body, err := reader.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId})
					var typed *jobinspect.WorkspaceDiffError
					require.ErrorAs(t, err, &typed)
					require.Equal(t, "protocol", typed.Code)
					require.Nil(t, m)
					require.Nil(t, body)
				}
			}
		})
	}
}

func TestWorkspaceDiffNativePatchlessObserverAttemptsAndCutoffs(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := savedBackend(t, backend)
			index := 3
			f, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{FixedBase: true, RetryTask: &index, Transform: func(s []workspacedifftest.Step) []workspacedifftest.Step { return observerSteps(s, true) }})
			require.NoError(t, err)
			api := &jobinspect.Service{Runtime: rt, Tenant: f.Job.TenantId}
			log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
			require.NoError(t, err)
			task := log.Tasks[index]
			require.Equal(t, []int{1, 2}, task.Attempts)
			run, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: f.Job, IncludeInputs: true, IncludeOutputs: true, IncludeArtifacts: true, IncludeAttemptInputs: true})
			require.NoError(t, err)
			var chapter int64 = -1
			for _, e := range run.Execution {
				if e.Record.ID == task.ID+"/attempt:1" {
					if e.Record.Kind == "attempt" {
						chapter = e.Ordinal
					}
					if e.Record.Kind == "result" {
						require.Equal(t, chapter, e.Ordinal)
					}
				}
			}
			require.GreaterOrEqual(t, chapter, int64(0))
			var original, latest *jobinspect.WorkspaceDiff
			for _, n := range []int{1, 2, 1} {
				var before *jobinspect.WorkspaceDiff
				for _, boundary := range []string{"before", "after"} {
					q := jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: &task.ID, Attempt: &n, Boundary: &boundary}}
					m, body, err := api.OpenWorkspaceDiff(ctx, q)
					require.NoError(t, err)
					require.Equal(t, "available", m.Status, "%v", m.Reasons)
					data, err := io.ReadAll(body)
					require.NoError(t, err)
					require.NoError(t, body.Close())
					require.Equal(t, f.Oracle, data)
					require.Equal(t, n, m.Resolved.Attempt)
					require.Equal(t, n, m.StateProducer.Attempt)
					require.Equal(t, log.Tasks[2].ID, m.Source.Producer.Task)
					require.Equal(t, 1, m.Source.Producer.Attempt)
					if boundary == "before" {
						before = m
						continue
					}
					require.Equal(t, before.Workspace, m.Workspace)
					require.NotEqual(t, before.Cutoff, m.Cutoff)
					if n == 1 {
						if original != nil {
							require.Equal(t, original, m)
						}
						original = m
					} else {
						latest = m
						require.NotEqual(t, original.Cutoff, latest.Cutoff)
						require.Equal(t, original.Source, m.Source)
					}
				}
			}
		})
	}
}

func TestWorkspaceDiffNativePatchlessObserverEnvelopeVersions(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := savedBackend(t, backend)
			f, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{FixedBase: true, Transform: func(s []workspacedifftest.Step) []workspacedifftest.Step {
				s = observerSteps(s, true)
				s[4].EnvelopeVersion = 99
				return s
			}})
			require.NoError(t, err)
			guard := &observerReadGuard{Runtime: rt}
			api := &jobinspect.Service{Runtime: guard, Tenant: f.Job.TenantId}
			_, body, err := api.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId})
			var typed *jobinspect.WorkspaceDiffError
			require.ErrorAs(t, err, &typed)
			require.Equal(t, "protocol", typed.Code)
			require.Nil(t, body)
			require.Zero(t, guard.patches)
			require.Zero(t, guard.bundles)
		})
	}
}
