package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Keep the historical schema in an isolated registry. Never rewrite retained
// input to satisfy the restored schema: replay and restart must consume it as is.
func TestHistoricalGoalIdentityExecutionRestartAndReplay(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, duplicate := range []string{"", "stale-payload-goal"} {
			t.Run(backend+"/"+duplicate, func(t *testing.T) {
				ctx := celltest.Context(context.Background())
				rt := newTimeoutReproRuntime(t, backend)
				registry := rt.WorkflowRuntime.(jobdb.JobSchemaRegistry)
				var schema map[string]any
				require.NoError(t, json.Unmarshal(jobdbschema.Schema(), &schema))
				properties := schema["firstChapterShape"].(map[string]any)["$defs"].(map[string]any)["startJob"].(map[string]any)["properties"].(map[string]any)
				properties["goal_id"] = map[string]any{"type": "string"}
				oldSchema, err := json.Marshal(schema)
				require.NoError(t, err)
				registered, err := registry.RegisterJobSchema(ctx, jobdb.RegisterJobSchemaRequest{TenantId: "tenant", Schema: oldSchema})
				require.NoError(t, err)
				require.Equal(t, "sha256:3b77b87e6349ecd231839a13ddfa30d0da797cb019ff56dd81c5b7260c394750", registered.SchemaHash)
				celltest.Create(t, rt, "tenant", "test", "example.test/test")
				input := workflowctl.StartJob{TenantId: "tenant", Cell: "test", CellRepo: "example.test/test", RecipeName: "identity", GoalID: duplicate, JobContext: contextual.JobContext{Workflow: contextual.WorkflowContext{GoalID: "stale-context-goal"}}}
				data := jobdb.NewTaskDataOrPanic(input, jobdb.NewArtifactFromBytes("identity"+starter.RecipeArtifactSuffix, []byte(`id: identity
schema_version: 2
sequence:
 - id: checkpoint
   sequence: []
 - id: identity
   sequence: []
   outputs:
    goal: '${{ context.workflow.goal_id }}'
outputs:
 goal: '${{ sequence.identity.outputs.goal }}'
`)))
				original, err := data.GetData()
				require.NoError(t, err)
				handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: "recipe", Cell: "test", CellRepo: "example.test/test", Data: data, Schema: &jobdb.JobSchemaSelector{Hash: registered.SchemaHash}, RunPolicy: jobdb.DefaultRunPolicy()}})
				require.NoError(t, err)
				_, err = runGitLifecycle(t, goalCompletionInterruption{rt}, handle.JobKey)
				require.ErrorContains(t, err, "injected interruption")
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("tenant").PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{})).BuildEngine()
				require.NoError(t, err)
				history, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: handle.JobKey})
				require.NoError(t, err)
				var scope string
				for _, e := range history.Execution {
					if e.Record.Kind == "scope" && strings.Contains(e.Record.ID, `scope:"sequence:identity"`) {
						scope = e.Record.ID
					}
				}
				require.NotEmpty(t, scope)
				submitter := jobdbschema.WorkflowEngine{Engine: engine, Registry: registry}
				var refs []jobref.Ref
				for _, restartScope := range []string{starter.SubmissionRestartScopeID, scope} {
					for _, mode := range []string{"continue", "new"} {
						ref, err := starter.RestartRecipeJob(ctx, submitter, handle.JobKey, restartScope, nil, starter.RestartOptions{Goal: mode, RequestID: fmt.Sprintf("restart-%s-%s", restartScope, mode)})
						require.NoError(t, err)
						if mode == "continue" {
							require.Equal(t, handle.GoalID, ref.GoalID)
						} else {
							require.NotEqual(t, handle.GoalID, ref.GoalID)
						}
						refs = append(refs, ref)
					}
				}
				for _, ref := range refs {
					restarted, err := runGitLifecycle(t, rt, ref.Key())
					require.NoError(t, err)
					require.Equal(t, ref.GoalID, gitLifecycleOutput(t, restarted)["goal"])
					info, err := rt.GetJob(ctx, ref.Key())
					require.NoError(t, err)
					require.Equal(t, ref.GoalID, info.GoalID)
					page, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, JobKeys: []jobdb.JobKey{ref.Key()}})
					require.NoError(t, err)
					require.Len(t, page.Jobs, 1)
					require.Equal(t, ref.GoalID, joblist.JobFromSummary(page.Jobs[0]).GoalID)
					inspected, err := (&jobinspect.Service{Runtime: rt, Tenant: "tenant"}).Inspect(ctx, jobinspect.Request{Operation: "show", JobID: ref.JobID})
					require.NoError(t, err)
					require.Equal(t, ref.GoalID, inspected.Job.Goal)
					chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: ref.Key()})
					require.NoError(t, err)
					require.NotEmpty(t, chapters)
					retained, ok := chapters[0].Body.(jobdb.JobStartChapter)
					require.True(t, ok)
					require.Equal(t, []byte(original), []byte(retained.Input.Data))
				}
				out, err := runGitLifecycle(t, rt, handle.JobKey)
				require.NoError(t, err)
				require.Equal(t, handle.GoalID, gitLifecycleOutput(t, out)["goal"])
				replay, err := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: handle.JobKey, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
				require.NoError(t, err)
				raw, err := replay.GetData()
				require.NoError(t, err)
				var replayed map[string]any
				require.NoError(t, json.Unmarshal(raw, &replayed))
				require.Equal(t, handle.GoalID, replayed["goal"])
				// The original input remains byte-for-byte unchanged after execution/replay.
				chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
				require.NoError(t, err)
				require.NotEmpty(t, chapters)
				body, ok := chapters[0].Body.(jobdb.JobStartChapter)
				require.True(t, ok)
				require.Equal(t, []byte(original), []byte(body.Input.Data))
			})
		}
	}
}

// Leave a fully executed job runnable, so continue restarts are prepared while
// its goal is still open. Successful goals correctly reject new continuations.
type goalCompletionInterruption struct{ jobdb.WorkflowRuntime }
type goalCompletionLease struct{ jobdb.ExecutionLease }

func (r goalCompletionInterruption) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	lease, err := r.WorkflowRuntime.GetJobLease(ctx, req)
	if err != nil {
		return nil, err
	}
	return goalCompletionLease{lease}, nil
}
func (l goalCompletionLease) Complete(ctx context.Context, req jobdb.CompleteExecutionRequest) error {
	if err := l.ExecutionLease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}); err != nil {
		return err
	}
	return fmt.Errorf("injected interruption before completion")
}

func (l goalCompletionLease) LeaseToken() string {
	if v, ok := l.ExecutionLease.(interface{ LeaseToken() string }); ok {
		return v.LeaseToken()
	}
	return ""
}
func (l goalCompletionLease) LeaseWorkerID() string {
	if v, ok := l.ExecutionLease.(interface{ LeaseWorkerID() string }); ok {
		return v.LeaseWorkerID()
	}
	return ""
}
