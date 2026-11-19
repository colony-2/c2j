package story_test

import (
	"context"
	"fmt"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/jobrestart"
	restartservice "github.com/colony-2/c2j/pkg/jobrestart/service"
	coreops "github.com/colony-2/c2j/pkg/ops"
	coretask "github.com/colony-2/c2j/pkg/task"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/hookpolicy"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type submissionSetupTask struct {
	name  string
	calls atomic.Int32
	run   func(jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error)
}

func (w *submissionSetupTask) Name() string { return w.name }
func (w *submissionSetupTask) Run(ctx jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	w.calls.Add(1)
	return w.run(ctx, data)
}

func TestSubmissionRestartRerunsSetup(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		for _, failSource := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/source_failure=%t", backend, failSource), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 30*time.Second)
				defer cancel()
				var rt jobdb.ConsumerRuntime
				if backend == "toy" {
					rt = toy.New()
				} else {
					rt = storyTestRuntime(t, ctx, backend)
				}
				celltest.Create(t, rt, "submission", "test", "example.test/test")
				cellWorker := compiler.NewCellContextTaskWorker()
				cell := &submissionSetupTask{name: cellWorker.Name(), run: cellWorker.Run}
				version := 1
				source := &submissionSetupTask{name: compiler.RootSourceResolutionTaskType, run: func(jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error) {
					if failSource && version == 1 {
						return nil, fmt.Errorf("source temporarily unavailable")
					}
					return jobdb.NewTaskData(compiler.ResolvedRecipeSource{
						RecipeSourceResolution: compiler.RecipeSourceResolution{SourceKind: compiler.RecipeSourceKindServerRef, SubmittedSelector: "source-ref", ResolvedSelector: fmt.Sprintf("source-ref@v%d", version)},
						RecipeYAML: fmt.Sprintf(`id: resolved
source_hooks: from_current
input_schema:
 value: {type: string}
 api_key: {type: string}
inputs:
 value: '${{ inputs.value }}'
state:
 initial: done
 states:
  done:
   sequence: []
  unused_hook:
   include: cell://.c2/hooks/verify.yaml
outputs:
 value: '${{ inputs.value }}'
 version: %d
`, version),
					})
				}}
				policy := &submissionSetupTask{name: "recipe_hook_policy", run: func(jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error) {
					return jobdb.NewTaskData(hookpolicy.Snapshot{Revision: fmt.Sprintf("policy-v%d", version), Repository: "example.test/test"})
				}}
				worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{RootSourceResolver: compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{})})
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("submission").PlusWorkers(worker, cell, source, policy).BuildEngine()
				require.NoError(t, err)
				svc, err := story.New(story.ServiceConfig{Engine: engine, SchemaRegistry: rt.(jobdb.JobSchemaRegistry)})
				require.NoError(t, err)
				handle, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "submission", JobType: "recipe", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(workflowctl.StartJob{
					TenantId: "submission", RecipeName: "source-ref", Cell: "test", Inputs: map[string]any{"value": "submitted", "api_key": "sensitive fixture"},
				})})
				require.NoError(t, err)
				unstarted, err := svc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{ProjectID: "submission", JobID: handle.JobKey.JobId})
				require.NoError(t, err)
				require.Len(t, unstarted.RestartScopes, 1, "the engine records its header at submission, before the worker runs")
				require.Equal(t, starter.SubmissionRestartScopeID, unstarted.RestartScopes[0].ScopeID)
				run := func(key jobdb.JobKey, want jobworkflow.JobRunStatus) {
					t.Helper()
					runner, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: []jobworkflow.TaskWorker{cell, source, policy}, WorkerID: "test", LeaseDuration: time.Minute})
					require.NoError(t, err)
					out, err := runner.Run(nil)
					require.NoError(t, err)
					if out.Status != want {
						info, _ := rt.GetJob(ctx, key)
						t.Logf("job after run: route=%+v wait=%+v", info.NextRoute, info.ExecutionState.TaskWait)
					}
					require.Equal(t, want, out.Status, "%v", out.JobError)
				}
				want := jobworkflow.JobRunCompleted
				if failSource {
					want = jobworkflow.JobRunFailed
				}
				run(handle.JobKey, want)
				before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
				require.NoError(t, err)
				st, err := svc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{ProjectID: "submission", JobID: handle.JobKey.JobId})
				require.NoError(t, err)
				require.Equal(t, starter.SubmissionRestartScopeID, st.RestartScopes[0].ScopeID)
				if failSource {
					require.Len(t, st.RestartScopes, 1, "submission restart works even without a recipe scope")
				}
				snapshot, err := svc.GetRestartContext(ctx, story.GetRestartContextRequest{ProjectID: "submission", JobID: handle.JobKey.JobId, ScopeID: st.RestartScopes[0].ScopeID})
				require.NoError(t, err)
				require.Equal(t, map[string]any{"value": "submitted", "api_key": "[REDACTED]"}, snapshot.Visible["inputs"])
				require.Contains(t, snapshot.RedactedPaths, "/visible/inputs/api_key")
				require.EqualValues(t, 1, cell.calls.Load())
				require.EqualValues(t, 1, source.calls.Load(), "inspection never resolves the source")
				version = 2
				req := story.RestartRecipeJobRequest{ProjectID: "submission", JobID: handle.JobKey.JobId, ScopeID: st.RestartScopes[0].ScopeID, RequestID: uuid.NewString()}
				if !failSource {
					req.Goal.Mode = "new"
				}
				restarted, err := svc.RestartRecipeJob(ctx, req)
				require.NoError(t, err)
				if failSource {
					require.Equal(t, handle.GoalID, restarted.GoalID, "repair continues the original goal")
				}
				key := jobdb.JobKey{TenantId: restarted.TenantID, JobId: restarted.JobID}
				run(key, jobworkflow.JobRunCompleted)
				require.EqualValues(t, 2, cell.calls.Load())
				require.EqualValues(t, 2, source.calls.Load())
				policyCalls := int32(2)
				if failSource {
					policyCalls = 1
				}
				require.Equal(t, policyCalls, policy.calls.Load(), "hook policy is captured afresh")
				version = 3 // Replay must still show the recorded v2 result.
				fresh, err := svc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{ProjectID: key.TenantId, JobID: key.JobId})
				require.NoError(t, err)
				require.Equal(t, "source-ref@v2", fresh.Recipe.Source.ResolvedSelector)
				require.Equal(t, "submitted", fresh.Root.Output.(map[string]any)["value"])
				require.EqualValues(t, 2, fresh.Root.Output.(map[string]any)["version"])
				require.EqualValues(t, 2, source.calls.Load(), "replay does not resolve the new source")
				require.Equal(t, policyCalls, policy.calls.Load(), "replay does not refresh policy")
				after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
				require.NoError(t, err)
				require.Equal(t, before, after)
			})
		}
	}
}

// Counts only the API under test, separately from fixture and assertion reads.
type submissionHistoryEngine struct {
	jobworkflow.Engine
	t              *testing.T
	reads, replays int
}

func (e *submissionHistoryEngine) GetJobRun(ctx context.Context, q jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error) {
	e.reads++
	return e.Engine.GetJobRun(ctx, q)
}
func (e *submissionHistoryEngine) ReplayJobRun(ctx context.Context, q jobworkflow.ReplayRunRequest) (jobdb.JobData, error) {
	e.replays++
	return e.Engine.ReplayJobRun(ctx, q)
}
func (e *submissionHistoryEngine) SubmitRestartJob(ctx context.Context, q jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	require.Zero(e.t, q.Job.LastStepToKeep)
	require.Nil(e.t, q.Job.ExecutionRestart, "both selectors must request fresh execution")
	return e.Engine.SubmitRestartJob(ctx, q)
}

type submissionArtifactRuntime struct {
	jobdb.ConsumerRuntime
	reads int
}

func (r *submissionArtifactRuntime) OpenArtifact(ctx context.Context, ref jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	r.reads++
	return r.ConsumerRuntime.OpenArtifact(ctx, ref)
}

type forbiddenSubmissionResolver struct{ compiler.RecipeSourceResolver }

func (forbiddenSubmissionResolver) Resolve(context.Context, string, string) (compiler.RecipeSourceResolution, error) {
	panic("submission inspection must not resolve recipe sources")
}

func submissionArtifactContents(t *testing.T, ctx context.Context, rt jobdb.ConsumerRuntime, key jobdb.JobKey, chapters []jobdb.Chapter) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	for _, chapter := range chapters {
		for _, artifact := range chapter.Artifacts {
			reader, err := rt.OpenArtifact(ctx, jobdb.ArtifactRef{JobKey: key, Ordinal: chapter.Ordinal, Name: artifact.Name, Digest: artifact.Digest})
			require.NoError(t, err)
			stream, err := reader.Open()
			require.NoError(t, err)
			data, err := io.ReadAll(stream)
			require.NoError(t, err)
			require.NoError(t, stream.Close())
			result[fmt.Sprintf("%d/%s", chapter.Ordinal, artifact.Name)] = data
		}
	}
	return result
}

func TestSubmissionOnlyRestartExecution(t *testing.T) {
	coreops.Register(coreops.NewActivityMappedOpV2[restartProbeInput, map[string]any](coreops.OpMetadata{Type: restartProbeOp}, func(coreops.OpDependencies, context.Context, restartProbeInput) (map[string]any, error) {
		panic("fixture operations must run through task worker")
	}))
	const recipeYAML = `id: fixture
source_hooks: from_current
input_schema:
 value: {type: string}
 api_key: {type: string}
inputs:
 value: '${{ inputs.value }}'
state:
 initial: work
 states:
  work:
   inputs: {value: '${{ inputs.value }}'}
   sequence:
    - id: first
      op: restart_context_probe
      inputs: {value: '${{ inputs.value }}'}
    - id: second
      op: restart_context_probe
      inputs: {value: '${{ sequence.first.outputs.value }}'}
   outputs:
    value: '${{ sequence.second.outputs.value }}'
  unused_hook:
   include: cell://.c2/hooks/verify.yaml
outputs:
 value: '${{ states.work.outputs.value }}'
`
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		for _, state := range []string{"ready", "cancelled", "executed"} {
			for _, path := range []string{"story", "restart-service"} {
				for _, selector := range []string{"scope", "chapter"} {
					t.Run(fmt.Sprintf("%s/%s/%s/%s", backend, state, path, selector), func(t *testing.T) {
						ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 30*time.Second)
						defer cancel()
						var rt jobdb.ConsumerRuntime
						if backend == "toy" {
							rt = toy.New()
						} else {
							rt = storyTestRuntime(t, ctx, backend)
						}
						// storyTestRuntime's remote mode uses httptest + remote.NewConsumerServer(SQLite).
						registry := rt.(jobdb.JobSchemaRegistry)
						countedRT := &submissionArtifactRuntime{ConsumerRuntime: rt}
						celltest.Create(t, rt, "submission", "test", "example.test/test")
						cellWorker := compiler.NewCellContextTaskWorker()
						cell := &submissionSetupTask{name: cellWorker.Name(), run: cellWorker.Run}
						policy := &submissionSetupTask{name: "recipe_hook_policy", run: func(jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error) {
							return jobdb.NewTaskData(hookpolicy.Snapshot{Revision: "fresh-policy", Repository: "example.test/test"})
						}}
						probe := &restartProbeTask{}
						worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{})
						engine, err := jobworkflow.NewEngineBuilder().WithRuntime(countedRT).WithWorkerTenantId("submission").PlusWorkers(worker, cell, policy, probe).BuildEngine()
						require.NoError(t, err)
						history := &submissionHistoryEngine{Engine: engine, t: t}
						svc, err := story.New(story.ServiceConfig{Engine: history, SchemaRegistry: registry})
						require.NoError(t, err)
						api := &restartservice.Service{Engine: history, Tenant: "submission", Resolver: forbiddenSubmissionResolver{}}
						api.Submit = func(ctx context.Context, key jobdb.JobKey, scope string, patch *coretask.ContextPatch, opts starter.RestartOptions) (jobref.Ref, error) {
							return starter.RestartRecipeJob(ctx, jobdbschema.WorkflowEngine{Engine: history, Registry: registry}, key, scope, patch, opts)
						}
						data := jobdb.NewTaskDataOrPanic(workflowctl.StartJob{TenantId: "submission", RecipeName: "fixture", Cell: "test", CellRepo: "example.test/test", Inputs: map[string]any{"value": "original", "api_key": "sensitive fixture"}},
							jobdb.NewArtifactFromBytes("fixture"+starter.RecipeArtifactSuffix, []byte(recipeYAML)), jobdb.NewArtifactFromBytes("extra.txt", []byte("original artifact\x00contents\n")))
						job := jobdb.SubmitJob{TenantId: "submission", JobType: "recipe", Cell: "test", CellRepo: "example.test/test", Data: data}
						require.NoError(t, jobdbschema.Register(ctx, registry, "submission"))
						require.NoError(t, jobdbschema.SetSubmitJobSchema(&job))
						var handle jobdb.JobHandle
						if state == "executed" {
							handle, err = engine.SubmitJob(ctx, job)
						} else {
							handle, err = rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: job, WorkerID: "fixture"})
						}
						require.NoError(t, err)
						key := handle.JobKey
						run := func(key jobdb.JobKey) {
							t.Helper()
							runner, err := jobworkflow.GetJobForRun(ctx, countedRT, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: []jobworkflow.TaskWorker{cell, policy, probe}, WorkerID: "test", LeaseDuration: time.Minute})
							require.NoError(t, err)
							out, err := runner.Run(nil)
							require.NoError(t, err)
							require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
						}
						expectedStatus := jobdb.JobStatusReady
						if state == "cancelled" {
							require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: key, Reason: "submission-only fixture", WorkerID: "fixture"}))
							expectedStatus = jobdb.JobStatusCancelled
						} else if state == "executed" {
							run(key)
							expectedStatus = jobdb.JobStatusCompleted
						}
						before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
						require.NoError(t, err)
						beforeRun, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true, IncludeArtifacts: true})
						require.NoError(t, err)
						beforeInfo, err := rt.GetJob(ctx, key)
						require.NoError(t, err)
						require.Equal(t, expectedStatus, beforeInfo.Status)
						if state != "executed" {
							require.Len(t, before, 1)
							require.Empty(t, beforeRun.Execution)
							require.Zero(t, before[0].Ordinal)
						} else {
							require.NotEmpty(t, beforeRun.Execution)
						}
						contents := submissionArtifactContents(t, ctx, rt, key, before)
						require.Equal(t, []byte(recipeYAML), contents["0/fixture"+starter.RecipeArtifactSuffix])
						require.Equal(t, []byte("original artifact\x00contents\n"), contents["0/extra.txt"])
						history.reads, history.replays, countedRT.reads = 0, 0, 0
						points, err := api.Restart(ctx, jobrestart.Request{Operation: "points", JobID: key.JobId})
						require.NoError(t, err)
						require.Equal(t, 1, history.reads)
						require.Zero(t, history.replays)
						require.Zero(t, countedRT.reads, "discovery must not read artifact bodies")
						require.Equal(t, starter.SubmissionRestartScopeID, points.Points.RestartScopes[0].ScopeID)
						if state != "executed" {
							require.Len(t, points.Points.RestartScopes, 1)
						}
						zero := int64(0)
						selectBy := jobrestart.Selector{Scope: starter.SubmissionRestartScopeID}
						if selector == "chapter" {
							selectBy = jobrestart.Selector{Chapter: &zero}
						}
						countedRT.reads = 0
						if path == "story" {
							snapshot, err := svc.GetRestartContext(ctx, story.GetRestartContextRequest{ProjectID: key.TenantId, JobID: key.JobId, ScopeID: selectBy.Scope})
							require.NoError(t, err)
							require.Equal(t, "original", snapshot.Visible["inputs"].(map[string]any)["value"])
							require.Equal(t, "[REDACTED]", snapshot.Visible["inputs"].(map[string]any)["api_key"])
							require.Zero(t, countedRT.reads, "submission context does not load recipe artifacts")
							st, err := svc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{ProjectID: key.TenantId, JobID: key.JobId})
							require.NoError(t, err)
							require.Equal(t, starter.SubmissionRestartScopeID, st.RestartScopes[0].ScopeID)
						} else {
							snapshot, err := api.Restart(ctx, jobrestart.Request{Operation: "context", JobID: key.JobId, Selector: selectBy})
							require.NoError(t, err)
							require.Equal(t, "original", snapshot.Context.Visible["inputs"].(map[string]any)["value"])
							require.Equal(t, "[REDACTED]", snapshot.Context.Visible["inputs"].(map[string]any)["api_key"])
							require.Zero(t, countedRT.reads)
						}
						// All setup and recipe operations must run again at the destination.
						cellCalls, policyCalls, probeCalls := cell.calls.Load(), policy.calls.Load(), probe.calls.Load()
						restart := func(requestID, goal string) (jobdb.JobKey, error) {
							if path == "story" {
								req := story.RestartRecipeJobRequest{ProjectID: key.TenantId, JobID: key.JobId, ScopeID: selectBy.Scope, ChapterOrdinal: selectBy.Chapter, RequestID: requestID}
								req.Goal.Mode = goal
								result, err := svc.RestartRecipeJob(ctx, req)
								if err != nil {
									return jobdb.JobKey{}, err
								}
								return jobdb.JobKey{TenantId: result.TenantID, JobId: result.JobID}, nil
							}
							result, err := api.Restart(ctx, jobrestart.Request{Operation: "submit", JobID: key.JobId, Selector: selectBy, RequestID: requestID, Goal: goal})
							if err != nil {
								return jobdb.JobKey{}, err
							}
							return result.Ref.Key(), nil
						}
						if state == "executed" {
							_, err := restart("inadmissible-continuation", "")
							require.ErrorIs(t, err, jobdb.ErrGoalConflict, "a succeeded goal still prevents ordinary continuation")
						}
						destination, err := restart("accepted-restart", "new")
						require.NoError(t, err)
						again, err := restart("accepted-restart", "new")
						require.NoError(t, err)
						require.Equal(t, destination, again)
						retained, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: destination})
						require.NoError(t, err)
						require.Len(t, retained, 1)
						require.Zero(t, retained[0].Ordinal)
						require.Equal(t, before[0].Body, retained[0].Body)
						require.Equal(t, before[0].Artifacts, retained[0].Artifacts)
						require.Equal(t, contents, submissionArtifactContents(t, ctx, rt, key, before), "source bytes remain readable")
						require.Equal(t, contents["0/extra.txt"], submissionArtifactContents(t, ctx, rt, destination, retained)["0/extra.txt"])
						require.Equal(t, []byte(recipeYAML), submissionArtifactContents(t, ctx, rt, destination, retained)["0/fixture"+starter.RecipeArtifactSuffix])
						run(destination)
						require.Equal(t, cellCalls+1, cell.calls.Load(), "cell setup runs afresh")
						require.Equal(t, policyCalls+1, policy.calls.Load(), "hook policy runs afresh")
						require.Equal(t, probeCalls+2, probe.calls.Load(), "every recipe task runs afresh")
						fresh, err := svc.GetJobRunStory(ctx, story.GetJobRunStoryRequest{ProjectID: destination.TenantId, JobID: destination.JobId})
						require.NoError(t, err)
						require.Equal(t, "original", fresh.Root.Output.(map[string]any)["value"])
						freshRun, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: destination})
						require.NoError(t, err)
						require.NotEmpty(t, freshRun.Execution)
						require.EqualValues(t, 1, freshRun.Execution[0].Ordinal)
						require.Equal(t, "header", freshRun.Execution[0].Record.Kind)
						require.Greater(t, len(freshRun.Execution), 1)
						again, err = restart("accepted-restart", "new")
						require.NoError(t, err)
						require.Equal(t, destination, again, "receipt replay survives completed execution")
						_, err = restart("accepted-restart", "")
						require.Error(t, err, "changed request parameters must conflict")
						after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
						require.NoError(t, err)
						require.Equal(t, before, after)
						afterRun, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true, IncludeArtifacts: true})
						require.NoError(t, err)
						require.Equal(t, beforeRun, afterRun)
						afterInfo, err := rt.GetJob(ctx, key)
						require.NoError(t, err)
						// Compare public data values, not lazy wrappers containing fresh error stacks.
						if beforeInfo.Data != nil {
							require.NotNil(t, afterInfo.Data)
							beforeData, beforeErr := beforeInfo.Data.GetData()
							afterData, afterErr := afterInfo.Data.GetData()
							require.Equal(t, beforeData, afterData)
							if beforeErr == nil {
								require.NoError(t, afterErr)
							} else {
								require.EqualError(t, afterErr, beforeErr.Error())
							}
						} else {
							require.Nil(t, afterInfo.Data)
						}
						beforeInfo.Data, afterInfo.Data = nil, nil
						require.Equal(t, beforeInfo, afterInfo)
						require.Equal(t, contents, submissionArtifactContents(t, ctx, rt, key, after))
					})
				}
			}
		}
	}
}
