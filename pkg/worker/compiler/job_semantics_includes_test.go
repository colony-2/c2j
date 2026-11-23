package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Start from persisted input, rather than fresh preparation, so absent/1
// snapshots cannot accidentally inherit today's semantics. The native compiler,
// activity worker and included definitions must all interpret the same bytes.
func TestExactJobIncludedSnapshotPolicies(t *testing.T) {
	ws := workspaceTestWorker(t)
	for _, backend := range []string{"sqlite", "remote"} {
		for _, form := range []string{"direct", "eager", "late"} {
			for _, version := range []int{0, 1, 2} {
				for _, policy := range []string{"exact", "implicit", "goal"} {
					t.Run(fmt.Sprintf("%s/%s/version=%d/%s", backend, form, version, policy), func(t *testing.T) {
						f := newLateGitFixture(t)
						wait := "wait: {on: job}\n"
						if policy == "implicit" {
							wait = ""
						} else if policy == "goal" {
							wait = "wait: {on: goal}\n"
						}
						// A skipped child isolates interpretation from publication. Its
						// effective wait remains observable in the real activity output.
						child := "id: child\nsequence:\n- id: fanout\n  child_group:\n    mode: run_and_get_result\n" + indentLate(wait, "    ") + "    children: [{key: skipped, recipe: unused, when: false}]\noutputs: {condition: '${{ sequence.fanout.outputs.children[0].wait.condition }}'}\n"
						body := child
						if form != "direct" {
							commit := commitHookFile(t, f.dir, "child.yaml", child)
							selector := fmt.Sprintf("include: %q", f.selector("child.yaml", commit))
							if form == "late" {
								selector = fmt.Sprintf("include: {bind: late, recipe: %q}", f.selector("child.yaml", commit))
							}
							body = "id: root\nsequence:\n- id: call\n  " + selector + "\noutputs: {condition: '${{ sequence.call.outputs.condition }}'}\n"
						}
						rec := f.expand(t, body)
						frozen, err := marshalRecipeYAML(rec)
						require.NoError(t, err)
						rt := newTimeoutReproRuntime(t, backend)
						ctx := lateTestContext()
						celltest.Create(t, rt, "snapshots", "test", celltest.Repo(f.dir))
						start := workflowctl.StartJob{
							TenantId: "snapshots", RecipeName: "root", JobSemanticsVersion: version,
							JobContext: contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: f.dir, BaseRef: f.initial, ResolvedBaseHash: f.initial}},
						}
						data := jobdb.NewTaskDataOrPanic(start, jobdb.NewArtifactFromBytes("root"+starter.RecipeArtifactSuffix, frozen))
						original := append([]byte(nil), data.GetDataOrPanic()...)
						if version == 0 {
							require.NotContains(t, string(original), "job_semantics_version")
						}
						handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "snapshots", Cell: "test", JobType: starter.RecipeJobType, Data: data}})
						require.NoError(t, err)
						w := NewRecipeJobWorker(RecipeJobWorkerOptions{})
						guard := lateGuard(rt)
						var activityCalls atomic.Int32
						tasks := lateRealTasks(t, guard, ws)
						for i, task := range tasks {
							if strings.HasPrefix(task.Name(), "recipe.child_group.") {
								tasks[i] = exactSnapshotActivity{TaskWorker: task, calls: &activityCalls}
							}
						}
						out := lateRun(t, guard, handle.JobKey, w, tasks...)
						accepted := policy == "exact" || (policy == "implicit" && version == 2)
						if !accepted {
							require.Equal(t, jobworkflow.JobRunFailed, out.Status, "%v", out.JobError)
							require.ErrorContains(t, out.JobError, "job_semantics_version")
							require.Zero(t, activityCalls.Load(), "retired syntax must not dispatch the incompatible activity")
							_, _, history := lateRecordOutputs(t, rt, handle.JobKey)
							if version == 2 || form == "late" {
								for _, event := range history.Execution {
									require.NotContains(t, string(event.Record.Descriptor), "recipe.child_group.", "static admission rejects before declaring the incompatible activity")
								}
							}
						} else {
							require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
							require.Positive(t, activityCalls.Load(), "exercise the native activity's saved policy interpretation")
							condition := "complete"
							if version == 2 {
								condition = "success"
							}
							var output map[string]any
							require.NoError(t, json.Unmarshal(out.Output.GetDataOrPanic(), &output))
							require.Equal(t, condition, output["condition"])
							f.available.Store(false)
							before := f.reads.Load()
							require.NoError(t, probeReplay(t, rt, handle.JobKey, NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})))
							require.Equal(t, before, f.reads.Load(), "replay uses the accepted include, not live source")
							if policy == "exact" {
								_, _, history := lateRecordOutputs(t, rt, handle.JobKey)
								var boundary string
								for _, event := range history.Execution {
									if event.Record.Kind == "task" && strings.Contains(string(event.Record.Descriptor), "recipe.child_group.run_and_get_result:start") {
										boundary = event.Record.ID
										break
									}
								}
								require.NotEmpty(t, boundary)
								// Retain the captured include while rerunning the activity.
								// The legacy restart contract must explicitly be independent.
								restarted, err := rt.SubmitRestartJob(ctx, jobdb.SubmitRestartJobRequest{RequestID: "same-snapshot", Job: jobdb.SubmitRestartJob{PriorJobKey: handle.JobKey, ShareGoal: false, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: boundary}, Cell: "test"}})
								require.NoError(t, err)
								require.NotEqual(t, handle.GoalID, restarted.GoalID)
								again := lateRun(t, guard, restarted.JobKey, w, lateRealTasks(t, guard, ws)...)
								require.Equal(t, jobworkflow.JobRunCompleted, again.Status, "%v", again.JobError)
								output = nil
								require.NoError(t, json.Unmarshal(again.Output.GetDataOrPanic(), &output))
								require.Equal(t, condition, output["condition"])
								require.Equal(t, before, f.reads.Load())
								assertSnapshotInput(t, rt, restarted.JobKey, original)
							}
						}
						assertSnapshotInput(t, rt, handle.JobKey, original)
						jobs, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"snapshots"}, ParentJobIDs: []string{handle.JobKey.JobId}})
						require.NoError(t, err)
						require.Empty(t, jobs.Jobs, "neither rejected syntax nor a skipped child publishes a child job")
					})
				}
			}
		}
	}
}

type exactSnapshotActivity struct {
	jobworkflow.TaskWorker
	calls *atomic.Int32
}

func (w exactSnapshotActivity) Run(ctx jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	w.calls.Add(1)
	return w.TaskWorker.Run(ctx, data)
}

func assertSnapshotInput(t *testing.T, rt jobdb.ConsumerRuntime, key jobdb.JobKey, original []byte) {
	t.Helper()
	chapters, err := rt.ListChapters(lateTestContext(), jobdb.ListChaptersRequest{JobKey: key})
	require.NoError(t, err)
	require.NotEmpty(t, chapters)
	start, ok := chapters[0].Body.(jobdb.JobStartChapter)
	require.True(t, ok)
	require.Equal(t, original, []byte(start.Input.Data), "execution/restart must not rewrite saved interpretation")
}

// Exercise persisted input through the legacy JobDB engine, rather than only
// the fresh-submission guard. A later raw condition must prevent earlier effects.
func TestExactJobRawCELSavedExecutionNoEffects(t *testing.T) {
	testRawCELSavedExecutionNoEffects(t, "context.workflow.goal_id != ''", "retired live goal context")
}

func TestExactJobQuotedMarkerSavedExecutionNoEffects(t *testing.T) {
	for _, expression := range []string{
		`context.workflow.goal_id != '{{ literal }}'`,
		`'${{ literal }}' != context['workflow']['goal_id']`,
	} {
		t.Run(expression, func(t *testing.T) { testRawCELSavedExecutionNoEffects(t, expression, "retired live goal context") })
	}
}

func TestExactJobQuotedMarkerInvalidRawSavedExecutionNoEffects(t *testing.T) {
	testRawCELSavedExecutionNoEffects(t, `${{ context.workflow.goal_id }}`, "invalid raw CEL expression")
}

func testRawCELSavedExecutionNoEffects(t *testing.T, expression, expectedError string) {
	t.Helper()
	var effects atomic.Int32
	op := ops.NewActivityMappedOpV2[map[string]any, map[string]any](ops.OpMetadata{Type: "raw_cel_effect"}, func(_ ops.OpDependencies, _ context.Context, _ map[string]any) (map[string]any, error) {
		effects.Add(1)
		return map[string]any{}, nil
	})
	ws := workspaceTestWorker(t, op)
	f := newLateGitFixture(t)
	quoted, err := json.Marshal(expression)
	require.NoError(t, err)
	rec, err := recipe.LoadRecipeFromString([]byte("id: root\nsequence:\n- id: earlier\n  op: raw_cel_effect\n- id: invalid\n  when: " + string(quoted) + "\n  sequence: []\n"))
	require.NoError(t, err)
	frozen, err := marshalRecipeYAML(*rec)
	require.NoError(t, err)
	for _, backend := range []string{"sqlite", "remote"} {
		for _, version := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("%s/version=%d", backend, version), func(t *testing.T) {
				effects.Store(0)
				rt := newTimeoutReproRuntime(t, backend)
				ctx := lateTestContext()
				celltest.Create(t, rt, "raw-cel", "test", celltest.Repo(f.dir))
				start := workflowctl.StartJob{TenantId: "raw-cel", RecipeName: "root", JobSemanticsVersion: version,
					JobContext: contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: f.dir, BaseRef: f.initial, ResolvedBaseHash: f.initial}},
				}
				data := jobdb.NewTaskDataOrPanic(start, jobdb.NewArtifactFromBytes("root"+starter.RecipeArtifactSuffix, frozen))
				original := append([]byte(nil), data.GetDataOrPanic()...)
				handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "raw-cel", Cell: "test", JobType: starter.RecipeJobType, Data: data}})
				require.NoError(t, err)
				guard := lateGuard(rt.ConsumerRuntime)
				out := lateRun(t, guard, handle.JobKey, NewRecipeJobWorker(RecipeJobWorkerOptions{}), lateRealTasks(t, guard, ws)...)
				require.Equal(t, jobworkflow.JobRunFailed, out.Status, "%v", out.JobError)
				require.ErrorContains(t, out.JobError, expectedError)
				require.Zero(t, effects.Load(), "later raw CEL must be rejected before earlier effects")
				_, _, history := lateRecordOutputs(t, rt, handle.JobKey)
				for _, event := range history.Execution {
					require.NotContains(t, string(event.Record.Descriptor), "raw_cel_effect", "preflight must precede effect declaration")
				}
				assertSnapshotInput(t, rt, handle.JobKey, original)
			})
		}
	}
}
