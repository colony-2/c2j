package recipejob_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"

	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/colony-2/jobdb/pkg/jobdb/runtimetest"
	workflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Exercise the delivered storage suite through c2j's receipt projection rather
// than treating producer evidence as proof of consumer integration.
type selectedAdapter struct {
	jobdb.ConsumerRuntime
	jobdb.JobDeletionRuntime
	engine jobdbschema.WorkflowEngine
}

func (r selectedAdapter) SubmitJob(ctx context.Context, req jobdb.SubmitJobRequest) (jobdb.JobHandle, error) {
	ref, err := r.engine.SubmitJobRef(ctx, req.Job)
	return jobdb.JobHandle{JobKey: ref.Key(), GoalID: ref.GoalID, Cell: ref.Cell, CellID: ref.CellID, CellRepo: ref.CellRepo}, err
}
func goalEngine(t *testing.T, rt jobdb.ConsumerRuntime) jobdbschema.WorkflowEngine {
	t.Helper()
	sdk, err := workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	registry, _ := rt.(jobdb.JobSchemaRegistry)
	return jobdbschema.WorkflowEngine{Engine: sdk, Registry: registry}
}
func TestSelectedGoalConformanceThroughC2J(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite"} {
		for _, transport := range []string{"direct", "http"} {
			t.Run(backend+"/"+transport, func(t *testing.T) {
				var rt jobdb.ConsumerRuntime = toy.New()
				if backend == "sqlite" {
					embedded, err := sqlite.StartEmbeddedRuntime(context.Background())
					require.NoError(t, err)
					t.Cleanup(func() { embedded.Shutdown() })
					rt = embedded.Runtime
				}
				if transport == "http" {
					h := remote.NewConsumerServer(rt)
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						h.ServeHTTP(w, r.WithContext(runtimetest.PrimaryGoalContext(r.Context())))
					}))
					t.Cleanup(s.Close)
					client, err := remote.New(s.URL, s.Client())
					require.NoError(t, err)
					rt = client
				}
				adapter := selectedAdapter{ConsumerRuntime: rt, JobDeletionRuntime: rt.(jobdb.JobDeletionRuntime), engine: goalEngine(t, rt)}
				runtimetest.RunPrimaryGoalSelectionConformance(t, adapter)
				if transport == "direct" {
					runtimetest.RunPrimaryGoalSelectionBoundaries(t, adapter)
				}
			})
		}
	}
}
func TestExactJobFreshGoalSelectionUnsupported(t *testing.T) {
	rt := toy.New()
	engine := goalEngine(t, rt)
	ctx := celltest.Context(context.Background())
	celltest.Create(t, rt, "tenant", "work", "example.test/work")
	_, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", JobID: "fresh", GoalID: "retired", Cell: "work", CellRepo: "example.test/work", RecipeName: "build"}, engine)
	require.ErrorContains(t, err, "goal selection is unsupported")
	_, err = rt.GetJob(ctx, jobdb.JobKey{TenantId: "tenant", JobId: "fresh"})
	require.ErrorIs(t, err, jobdb.ErrJobNotFound)
}

func TestSelectedGoalRejectionsHaveNoPublicSideEffects(t *testing.T) {
	for _, transport := range []string{"direct", "http"} {
		t.Run(transport, func(t *testing.T) {
			ctx := runtimetest.PrimaryGoalContext(context.Background())
			raw := toy.New()
			celltest.Create(t, raw, "tenant", "work", "example.test/work")
			celltest.Create(t, raw, "tenant", "other", "example.test/other")
			celltest.Create(t, raw, "different", "work", "example.test/work")
			var rt jobdb.ConsumerRuntime = raw
			if transport == "http" {
				h := remote.NewConsumerServer(raw)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					h.ServeHTTP(w, r.WithContext(runtimetest.PrimaryGoalContext(r.Context())))
				}))
				defer server.Close()
				client, err := remote.New(server.URL, server.Client())
				require.NoError(t, err)
				rt = client
			}
			engine := goalEngine(t, rt)
			source, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "source", JobType: "work", Cell: "work", CellRepo: "example.test/work", Data: jobdb.NewTaskDataOrPanic(nil)}})
			require.NoError(t, err)
			job, err := prepareLegacyRecipe(ctx, workflowctl.StartJob{TenantId: "tenant", JobID: "accepted", GoalID: source.GoalID, Cell: "work", CellRepo: "example.test/work", RecipeName: "build"}, starter.StartRecipeJobOptions{})
			require.NoError(t, err)
			before, err := rt.ListGoals(ctx, jobdb.ListGoalsRequest{TenantId: "tenant"})
			require.NoError(t, err)
			_, err = engine.SubmitJobRef(ctx, job)
			require.ErrorIs(t, err, jobdb.ErrGoalConflict)
			_, err = rt.GetJob(ctx, jobdb.JobKey{TenantId: "tenant", JobId: "accepted"})
			require.ErrorIs(t, err, jobdb.ErrJobNotFound)
			after, err := rt.ListGoals(ctx, jobdb.ListGoalsRequest{TenantId: "tenant"})
			require.NoError(t, err)
			require.Equal(t, before, after, "rejected admission must not change goals")
			require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: source.JobKey}))
			_, err = engine.SubmitJobRef(ctx, job)
			require.NoError(t, err)
			// A fresh-submit receipt cannot be reused as a Restart operation.
			_, err = engine.SubmitRestartJob(ctx, jobdb.SubmitRestartJobRequest{RequestID: "different-operation", Job: jobdb.SubmitRestartJob{PriorJobKey: source.JobKey, JobID: "accepted", Cell: "work", CellRepo: "example.test/work"}})
			require.ErrorIs(t, err, jobdb.ErrExistingJobMismatch)
			for _, test := range []struct {
				name   string
				mutate func(*jobdb.SubmitJob)
				want   error
			}{
				{"missing", func(j *jobdb.SubmitJob) { j.GoalID = "missing" }, jobdb.ErrGoalNotFound},
				{"tenant", func(j *jobdb.SubmitJob) { j.TenantId = "different" }, jobdb.ErrGoalNotFound},
				{"cell", func(j *jobdb.SubmitJob) { j.Cell = "other"; j.CellRepo = "example.test/other" }, jobdb.ErrCellConflict},
				{"repository", func(j *jobdb.SubmitJob) { j.CellRepo = "example.test/drift" }, jobdb.ErrCellRepoMismatch},
			} {
				t.Run(test.name, func(t *testing.T) {
					changed := job
					changed.JobID = "rejected-" + test.name
					test.mutate(&changed)
					before, err := rt.ListGoals(ctx, jobdb.ListGoalsRequest{TenantId: changed.TenantId})
					require.NoError(t, err)
					_, err = engine.SubmitJobRef(ctx, changed)
					require.ErrorIs(t, err, test.want)
					_, err = rt.GetJob(ctx, jobdb.JobKey{TenantId: changed.TenantId, JobId: changed.JobID})
					require.ErrorIs(t, err, jobdb.ErrJobNotFound)
					after, err := rt.ListGoals(ctx, jobdb.ListGoalsRequest{TenantId: changed.TenantId})
					require.NoError(t, err)
					require.Equal(t, len(before.Goals), len(after.Goals))
				})
			}
		})
	}
}

func TestSelectedGoalReceiptReplayWithLaterActiveSuccessor(t *testing.T) {
	for _, transport := range []string{"sqlite", "http"} {
		t.Run(transport, func(t *testing.T) {
			ctx := runtimetest.PrimaryGoalContext(context.Background())
			db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
			var rt jobdb.ConsumerRuntime = db
			if transport == "http" {
				handler := remote.NewConsumerServer(db)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					handler.ServeHTTP(w, r.WithContext(runtimetest.PrimaryGoalContext(r.Context())))
				}))
				t.Cleanup(server.Close)
				rt, err = remote.New(server.URL, server.Client())
				require.NoError(t, err)
			}
			celltest.Create(t, rt, "tenant", "work", "example.test/work")
			engine := goalEngine(t, rt)
			req := recipejob.BuildStartJobRequest{TenantID: "tenant", JobID: "first", Target: recipejob.ResolvedTarget{CellName: "work", CellRepo: "example.test/work", RepositorySource: "https://example.test/work.git", DefaultRef: "main"}, Recipe: "build"}
			source, err := recipejob.SubmitRecipeJob(ctx, req, engine)
			require.NoError(t, err)
			cancel := func(key jobdb.JobKey) {
				t.Helper()
				require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: key}))
				info, err := rt.GetJob(ctx, key)
				require.NoError(t, err)
				require.Equal(t, jobdb.JobStatusCancelled, info.Status)
			}
			cancel(source.Key())
			req.JobID = "accepted"
			start, err := recipejob.BuildStartJob(req)
			require.NoError(t, err)
			start.GoalID = source.GoalID
			start.Artifacts = []jobdb.Artifact{jobdb.NewArtifactFromBytes("input.txt", []byte("input café\n"))}
			job, err := prepareLegacyRecipe(ctx, start, starter.StartRecipeJobOptions{})
			require.NoError(t, err)
			descriptor, err := legacyDescriptor(ctx, job)
			require.NoError(t, err)
			wire, err := json.Marshal(descriptor)
			require.NoError(t, err)
			var saved starter.Descriptor
			require.NoError(t, json.Unmarshal(wire, &saved))
			accepted, err := saved.Submit(ctx, engine)
			require.NoError(t, err)
			require.Equal(t, jobref.Ref{TenantID: "tenant", JobID: "accepted", GoalID: source.GoalID, Cell: "work", CellID: source.CellID, CellRepo: "example.test/work"}, accepted)
			acceptedChapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: accepted.Key()})
			require.NoError(t, err)
			acceptedInput := acceptedChapters[0].Body.(jobdb.JobStartChapter).Input.Data
			require.JSONEq(t, string(saved.Input), string(acceptedInput))
			cancel(accepted.Key())
			req.JobID = "successor"
			next := job
			next.JobID = "successor"
			successor, err := engine.SubmitJobRef(ctx, next)
			require.NoError(t, err, "the pinned backend must permit this sequential successor setup")
			require.Equal(t, accepted.GoalID, successor.GoalID)
			active, err := rt.GetJob(ctx, successor.Key())
			require.NoError(t, err)
			require.Equal(t, jobdb.JobStatusReady, active.Status)
			replay, err := saved.Submit(ctx, engine)
			require.NoError(t, err)
			require.Equal(t, accepted, replay)
			t.Log("exact accepted descriptor replayed while later successor is READY")
			_, err = rt.FailGoal(ctx, jobdb.FailGoalRequest{GoalKey: jobdb.GoalKey{TenantId: accepted.TenantID, GoalID: accepted.GoalID}, Message: "closed"})
			require.NoError(t, err)
			replay, err = saved.Submit(ctx, engine)
			require.NoError(t, err)
			require.Equal(t, accepted, replay)
			chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: accepted.Key()})
			require.NoError(t, err)
			input := chapters[0].Body.(jobdb.JobStartChapter).Input
			require.Equal(t, acceptedInput, input.Data)
			require.Len(t, chapters[0].Artifacts, 1)
			stored := chapters[0].Artifacts[0]
			artifact, err := rt.OpenArtifact(ctx, jobdb.ArtifactRef{JobKey: accepted.Key(), Ordinal: chapters[0].Ordinal, Name: stored.Name, Digest: stored.Digest})
			require.NoError(t, err)
			reader, err := artifact.Open()
			require.NoError(t, err)
			defer reader.Close()
			data, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.Equal(t, "input café\n", string(data))
		})
	}
}

// Test-only reconstruction of already retained legacy requests. Production fresh
// preparation rejects selected goals; fixtures never use that path to select.
func prepareLegacyRecipe(ctx context.Context, start workflowctl.StartJob, opts starter.StartRecipeJobOptions) (jobdb.SubmitJob, error) {
	goal := start.GoalID
	start.GoalID = ""
	opts.GoalID = ""
	j, err := starter.PrepareRecipeJob(ctx, start, opts)
	j.GoalID = goal
	if j.Schema != nil {
		j.Schema.Schema = jobdbschema.Schema()
	}
	return j, err
}
func legacyDescriptor(ctx context.Context, j jobdb.SubmitJob) (starter.Descriptor, error) {
	d, err := starter.NewDescriptor(ctx, j)
	d.Version = 1
	return d, err
}
