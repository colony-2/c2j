package recipejob_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
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
	jobdb.WorkflowRuntime
	jobdb.JobDeletionRuntime
	engine jobdbschema.WorkflowEngine
}

func (r selectedAdapter) SubmitJob(ctx context.Context, req jobdb.SubmitJobRequest) (jobdb.JobHandle, error) {
	ref, err := r.engine.SubmitJobRef(ctx, req.Job)
	return jobdb.JobHandle{JobKey: ref.Key(), GoalID: ref.GoalID, Cell: ref.Cell, CellID: ref.CellID, CellRepo: ref.CellRepo}, err
}
func goalEngine(t *testing.T, rt jobdb.WorkflowRuntime) jobdbschema.WorkflowEngine {
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
				var rt jobdb.WorkflowRuntime = toy.New()
				if backend == "sqlite" {
					embedded, err := sqlite.StartEmbeddedRuntime(context.Background())
					require.NoError(t, err)
					t.Cleanup(func() { embedded.Shutdown() })
					rt = embedded.Runtime
				}
				if transport == "http" {
					h := remote.NewServer(rt)
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						h.ServeHTTP(w, r.WithContext(runtimetest.PrimaryGoalContext(r.Context())))
					}))
					t.Cleanup(s.Close)
					client, err := remote.New(s.URL, s.Client())
					require.NoError(t, err)
					rt = client
				}
				adapter := selectedAdapter{WorkflowRuntime: rt, JobDeletionRuntime: rt.(jobdb.JobDeletionRuntime), engine: goalEngine(t, rt)}
				runtimetest.RunPrimaryGoalSelectionConformance(t, adapter)
				if transport == "direct" {
					runtimetest.RunPrimaryGoalSelectionBoundaries(t, adapter)
				}
			})
		}
	}
}
func TestSelectedGoalFreshRecipeAndDescriptor(t *testing.T) {
	rt := toy.New()
	engine := goalEngine(t, rt)
	ctx := runtimetest.PrimaryGoalContext(context.Background())
	celltest.Create(t, rt, "tenant", "work", "example.test/work")
	req := recipejob.BuildStartJobRequest{TenantID: "tenant", JobID: "source", Target: recipejob.ResolvedTarget{CellName: "work", CellRepo: "example.test/work", RepositorySource: "https://example.test/work.git", DefaultRef: "main"}, Recipe: "build", Inputs: map[string]any{"prompt": "source"}}
	source, err := recipejob.SubmitRecipeJob(ctx, req, engine)
	require.NoError(t, err)
	req.JobID = "fresh"
	req.GoalID = source.GoalID
	req.Inputs = map[string]any{"prompt": "new instructions", "resume_from": map[string]any{"job": "source", "opaque": "exact"}}
	start, err := recipejob.BuildStartJob(req)
	require.NoError(t, err)
	job, err := starter.PrepareRecipeJob(ctx, start, starter.StartRecipeJobOptions{})
	require.NoError(t, err)
	d, err := starter.NewDescriptor(ctx, job)
	require.NoError(t, err)
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	var replay starter.Descriptor
	require.NoError(t, json.Unmarshal(raw, &replay))
	ref, err := replay.Submit(ctx, engine)
	require.NoError(t, err)
	require.Equal(t, source.GoalID, ref.GoalID)
	chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: ref.Key()})
	require.NoError(t, err)
	require.Len(t, chapters, 1)
	body := chapters[0].Body.(jobdb.JobStartChapter)
	var captured workflowctl.StartJob
	require.NoError(t, json.Unmarshal(body.Input.Data, &captured))
	require.Equal(t, req.Inputs, captured.Inputs)
	info, err := rt.GetJob(ctx, ref.Key())
	require.NoError(t, err)
	require.Empty(t, info.OtherGoalIDs)
	_, err = rt.FailGoal(ctx, jobdb.FailGoalRequest{GoalKey: jobdb.GoalKey{TenantId: "tenant", GoalID: ref.GoalID}, Message: "done"})
	require.NoError(t, err)
	again, err := replay.Submit(ctx, engine)
	require.NoError(t, err)
	require.Equal(t, ref, again)
	changed := replay
	changed.Job.GoalID = "missing"
	_, err = changed.Submit(ctx, engine)
	require.ErrorIs(t, err, jobdb.ErrExistingJobMismatch)
	req.JobID = "ordinary"
	req.GoalID = ""
	req.Inputs["goal_id"] = source.GoalID
	req.Parent = &jobcontext.Parent{TenantID: "tenant", JobID: "source", GoalID: source.GoalID}
	req.Dependencies = []jobdb.Dependency{{Target: jobdb.DependencyTarget{JobID: source.JobID}, Condition: jobdb.JobPrereqComplete}}
	ordinary, err := recipejob.SubmitRecipeJob(ctx, req, engine)
	require.NoError(t, err)
	require.NotEqual(t, source.GoalID, ordinary.GoalID)
}

func TestSelectedGoalRejectionsHaveNoPublicSideEffects(t *testing.T) {
	for _, transport := range []string{"direct", "http"} {
		t.Run(transport, func(t *testing.T) {
			ctx := runtimetest.PrimaryGoalContext(context.Background())
			raw := toy.New()
			celltest.Create(t, raw, "tenant", "work", "example.test/work")
			celltest.Create(t, raw, "tenant", "other", "example.test/other")
			celltest.Create(t, raw, "different", "work", "example.test/work")
			var rt jobdb.WorkflowRuntime = raw
			if transport == "http" {
				h := remote.NewServer(raw)
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
			job, err := starter.PrepareRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", JobID: "accepted", GoalID: source.GoalID, Cell: "work", CellRepo: "example.test/work", RecipeName: "build"}, starter.StartRecipeJobOptions{})
			require.NoError(t, err)
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
