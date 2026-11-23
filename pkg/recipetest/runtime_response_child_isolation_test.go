package recipetest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/scenariotest"
	inputop "github.com/colony-2/c2j/pkg/input"
	coreops "github.com/colony-2/c2j/pkg/ops"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestResponsePersistenceRootBeforeControlledChild(t *testing.T) {
	withRegisteredCoreOps(t, append(recipeops.GetOps(), inputop.GetOp(), commandop.GetOp())...)
	for _, structured := range []bool{false, true} {
		for _, source := range []string{"binding", "submission"} {
			t.Run(fmt.Sprintf("%t/%s", structured, source), func(t *testing.T) {
				c, target := responsePersistenceCase(structured, "healthy")
				childRecipe := target.Content
				controlled := c.Runtime.Responses[0]
				controlled.Submission.Pending.Job = "child"
				legacy := controlled
				legacy.Submission = nil
				legacy.Actor = &inputop.Actor{ID: "legacy-root-human", Kind: "human"}
				c.Runtime.Responses = []InputFixture{controlled, legacy}
				childSource := ScenarioJob{ChildBinding: "child"}
				spawn := "- id: spawn\n  op: command_execution\n  inputs: {run: unused}\n"
				if source == "submission" {
					childSource = ScenarioJob{Submission: &SubmissionSelection{Caller: "root", Task: TaskSelection{TaskType: "recipes.run:start", NodePath: "persistence/spawn", Attempt: 1, Occurrence: 1}, Index: 1}}
					spawn = "- id: spawn\n  op: recipes.run\n  inputs:\n    git_ref: main\n    recipes:\n    - {name: persistence, cell_name: root}\n"
				} else {
					c.Mocks.Ops = append(c.Mocks.Ops, OpMock{Match: OpMockMatch{NodePath: "persistence/spawn"}, Behavior: MockBehavior{Mode: "return", Effects: &FixtureEffects{Children: []ChildFixture{{Bind: "child", Recipe: "child.yaml", Cell: "root"}}}}})
				}
				// The legacy root wait must be answered before the selected child
				// can exist. Both jobs use the same authored input node and cell.
				target.Content = strings.Replace(target.Content, "- id: continuation\n", spawn+"- id: continuation\n", 1)
				for i := range c.Mocks.Ops {
					c.Mocks.Ops[i].Repeat = true
				}
				c.Runtime.Cells = map[string]CellFixture{"root": {Files: map[string]string{".c2/recipes/persistence.yaml": childRecipe}}}
				c.Runtime.Controls.Scenario = &Scenario{
					Jobs:     map[string]ScenarioJob{"root": {Root: true}, "child": childSource},
					Outcomes: map[string]ScenarioOutcome{"root": {Status: "completed"}, "child": {Status: "completed"}},
					Events:   []ScenarioEvent{{ID: "pending", Kind: "response", Response: "faulted", Phase: "pending"}, {ID: "attempted", Kind: "response", Response: "faulted", Phase: "attempted"}},
					Actions:  []ScenarioAction{{ID: "stop", Do: "stop_worker", AfterAll: []string{"pending"}}, {ID: "finish", Do: "start_worker", AfterAll: []string{"attempted"}}},
				}
				c.Assertions = []Assertion{{Type: "op_call_count", NodePath: "persistence/continuation", Value: 2}}
				fixtures := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(fixtures, "child.yaml"), []byte(childRecipe), 0600))
				result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), FixtureRoot: fixtures, WorkRoot: t.TempDir()}, "root-before-child", target, c, ExecutionOptions{Timeout: "20s"})
				require.Equal(t, "passed", result.Status, "%+v", result)
				s := result.Runtime.Controls.Scenario
				rootID, childID := s.Jobs["root"].Ref.JobID, s.Jobs["child"].Ref.JobID
				require.NotEmpty(t, childID)
				require.NotEqual(t, rootID, childID)
				require.Len(t, result.Runtime.Reviews, 2)
				rootReply, childReply := result.Runtime.Reviews[0], result.Runtime.Reviews[1]
				require.Equal(t, rootID, rootReply.JobID)
				require.Equal(t, "legacy-root-human", rootReply.Receipt.Actor.ID)
				require.Equal(t, childID, childReply.JobID)
				require.Equal(t, "synthetic-human", childReply.Receipt.Actor.ID)
				require.NotEqual(t, rootReply.RequestID, childReply.RequestID)
				require.Len(t, result.Runtime.Submissions, 1)
				a := result.Runtime.Submissions[0]
				require.True(t, a.Complete)
				require.Equal(t, childID, a.Pending.JobID)
				require.Equal(t, childReply.RequestID, a.Pending.RequestID)
				require.NotNil(t, a.Durable.Receipt)
				require.Equal(t, childReply.Receipt, a.Durable.Receipt)
			})
		}
	}
}

func TestResponsePersistenceChildBeforeControlledGrandchild(t *testing.T) {
	withRegisteredCoreOps(t, append(recipeops.GetOps(), inputop.GetOp(), commandop.GetOp())...)
	for _, structured := range []bool{false, true} {
		for _, parentSource := range []string{"binding", "submission"} {
			for _, source := range []string{"binding", "submission", "submission-chain"} {
				t.Run(fmt.Sprintf("%t/%s/%s", structured, parentSource, source), func(t *testing.T) {
					c, target := responsePersistenceCase(structured, "healthy")
					grandchildRecipe := target.Content
					controlled := c.Runtime.Responses[0]
					controlled.Submission.Pending.Job = "grandchild"
					legacy := controlled
					legacy.Submission = nil
					legacy.Actor = &inputop.Actor{ID: "legacy-parent-human", Kind: "human"}
					c.Runtime.Responses = []InputFixture{controlled, legacy}
					// Both creation paths must distinguish the submitting child from
					// its future descendant, even at the same authored input and cell.
					spawn := func(source, path, binding, name string) (string, ScenarioJob) {
						if source == "submission" {
							caller := "root"
							if binding == "grandchild" {
								caller = "parent"
							}
							return "- id: spawn\n  op: recipes.run\n  inputs:\n    git_ref: main\n    recipes:\n    - {name: " + name + ", cell_name: root}\n", ScenarioJob{Submission: &SubmissionSelection{Caller: caller, Task: TaskSelection{TaskType: "recipes.run:start", NodePath: path, Attempt: 1, Occurrence: 1}, Index: 1}}
						}
						c.Mocks.Ops = append(c.Mocks.Ops, OpMock{Match: OpMockMatch{NodePath: path}, Behavior: MockBehavior{Mode: "return", Effects: &FixtureEffects{Children: []ChildFixture{{Bind: binding, Recipe: name + ".yaml", Cell: "root"}}}}})
						return "- id: spawn\n  op: command_execution\n  inputs: {run: unused}\n", ScenarioJob{ChildBinding: binding}
					}
					rootSpawn, parentJob := spawn(parentSource, "root/spawn", "parent", "middle")
					childSource := source
					if source == "submission-chain" {
						childSource = "submission"
					}
					childSpawn, grandchildJob := spawn(childSource, "persistence/spawn", "grandchild", "leaf")
					jobs := map[string]ScenarioJob{"root": {Root: true}, "parent": parentJob, "grandchild": grandchildJob}
					outcomes := map[string]ScenarioOutcome{"root": {Status: "completed"}, "parent": {Status: "completed"}, "grandchild": {Status: "completed"}}
					files := map[string]string{"leaf.yaml": grandchildRecipe}
					if source == "submission-chain" {
						// The immediate caller is not created yet either. Its declared
						// submission ancestry still proves the waiting parent distinct.
						jobs["bridge"] = grandchildJob
						outcomes["bridge"] = ScenarioOutcome{Status: "completed"}
						childSpawn = strings.Replace(childSpawn, "name: leaf", "name: bridge", 1)
						bridgeSpawn, selected := spawn("submission", "bridge/spawn", "grandchild", "leaf")
						selected.Submission.Caller = "bridge"
						jobs["grandchild"] = selected
						files["bridge.yaml"] = "id: bridge\nversion: '1'\nsequence:\n" + bridgeSpawn
					}
					parentRecipe := strings.Replace(target.Content, "- id: continuation\n", childSpawn+"- id: continuation\n", 1)
					target.Content = "id: root\nversion: '1'\nsequence:\n" + rootSpawn
					for i := range c.Mocks.Ops {
						c.Mocks.Ops[i].Repeat = true
					}
					files["middle.yaml"] = parentRecipe
					cellFiles := map[string]string{}
					for name, content := range files {
						cellFiles[".c2/recipes/"+name] = content
					}
					c.Runtime.Cells = map[string]CellFixture{"root": {Files: cellFiles}}
					c.Runtime.Controls.Scenario = &Scenario{
						Jobs:     jobs,
						Outcomes: outcomes,
						Events:   []ScenarioEvent{{ID: "pending", Kind: "response", Response: "faulted", Phase: "pending"}, {ID: "attempted", Kind: "response", Response: "faulted", Phase: "attempted"}},
						Actions:  []ScenarioAction{{ID: "stop", Do: "stop_worker", AfterAll: []string{"pending"}}, {ID: "finish", Do: "start_worker", AfterAll: []string{"attempted"}}},
					}
					c.Assertions = []Assertion{{Type: "op_call_count", NodePath: "persistence/continuation", Value: 2}}
					fixtures := t.TempDir()
					for name, content := range files {
						require.NoError(t, os.WriteFile(filepath.Join(fixtures, name), []byte(content), 0600))
					}
					result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), FixtureRoot: fixtures, WorkRoot: t.TempDir()}, "child-before-grandchild", target, c, ExecutionOptions{Timeout: "20s"})
					raw, err := json.MarshalIndent(result, "", "  ")
					require.NoError(t, err)
					require.Equal(t, "passed", result.Status, "%s", raw)
					s := result.Runtime.Controls.Scenario
					parentID, grandchildID := s.Jobs["parent"].Ref.JobID, s.Jobs["grandchild"].Ref.JobID
					require.NotEmpty(t, parentID)
					require.NotEmpty(t, grandchildID)
					require.NotEqual(t, parentID, grandchildID)
					require.Len(t, result.Runtime.Reviews, 2)
					parentReply, childReply := result.Runtime.Reviews[0], result.Runtime.Reviews[1]
					require.Equal(t, parentID, parentReply.JobID)
					require.Equal(t, "legacy-parent-human", parentReply.Receipt.Actor.ID)
					require.Equal(t, grandchildID, childReply.JobID)
					require.Equal(t, "synthetic-human", childReply.Receipt.Actor.ID)
					require.NotEqual(t, parentReply.RequestID, childReply.RequestID)
					require.Len(t, result.Runtime.Submissions, 1)
					a := result.Runtime.Submissions[0]
					require.True(t, a.Complete)
					require.Equal(t, grandchildID, a.Pending.JobID)
					require.Equal(t, childReply.RequestID, a.Pending.RequestID)
					require.NotNil(t, a.Durable.Receipt)
					require.Equal(t, childReply.Receipt, a.Durable.Receipt)
				})
			}
		}
	}
}

func TestResponsePersistenceLateChildLegacyIsolation(t *testing.T) {
	withRegisteredCoreOps(t, append(recipeops.GetOps(), inputop.GetOp(), commandop.GetOp())...)
	for _, structured := range []bool{false, true} {
		for _, source := range []string{"binding", "submission"} {
			t.Run(fmt.Sprintf("%t/%s", structured, source), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 20*time.Second)
				defer cancel()
				c, target := responsePersistenceCase(structured, "healthy")
				target = repeatResponsePersistenceTarget(target, "0")
				controlled := c.Runtime.Responses[0]
				controlled.NodePath = "persistence/repeat/reply/input"
				controlled.Submission.Pending.Job = "child"
				controlled.Submission.Pending.Task.NodePath = controlled.NodePath
				controlled.Submission.AfterAll = nil
				legacy := controlled
				legacy.Submission = nil
				legacy.Actor = &inputop.Actor{ID: "legacy-human", Kind: "human"}
				c.Runtime.Responses = []InputFixture{controlled, legacy}
				childSource := ScenarioJob{ChildBinding: "child"}
				parentYAML := "id: parent\nversion: '1'\nsequence:\n- id: spawn\n  op: command_execution\n  inputs: {run: unused}\n"
				if source == "submission" {
					childSource = ScenarioJob{Submission: &SubmissionSelection{Caller: "root", Task: TaskSelection{TaskType: "recipes.run:start", NodePath: "parent/spawn", Attempt: 1, Occurrence: 1}, Index: 1}}
					parentYAML = "id: parent\nversion: '1'\nsequence:\n- id: spawn\n  op: recipes.run\n  inputs:\n    git_ref: main\n    recipes:\n    - {name: persistence, cell_name: root}\n"
				} else {
					c.Mocks.Ops = append(c.Mocks.Ops, OpMock{Match: OpMockMatch{NodePath: "parent/spawn"}, Behavior: MockBehavior{Mode: "return", Effects: &FixtureEffects{Children: []ChildFixture{{Bind: "child", Recipe: "child.yaml", Cell: "root"}}}}})
				}
				c.Runtime.Cells = map[string]CellFixture{"root": {Files: map[string]string{".c2/recipes/persistence.yaml": target.Content}}}
				c.Runtime.Controls.Scenario = &Scenario{Jobs: map[string]ScenarioJob{"root": {Root: true}, "child": childSource}}
				fixtures := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(fixtures, "child.yaml"), []byte(target.Content), 0600))
				f := &runtimeFixture{c: c, root: t.TempDir(), opts: HarnessOptions{FixtureRoot: fixtures}, cells: map[string]string{}, selected: map[string]int{}, callIndexes: map[string]int{}, used: map[int]struct{}{}, responses: map[int]bool{}, gateCounts: map[int]int{}, waiting: map[string]bool{}}
				f.initControls()
				require.NoError(t, f.seedCells(ctx, "root"))
				rt, closeRT, err := newCaseRuntime(ctx, f.root, "late-child", c.Runtime)
				require.NoError(t, err)
				defer closeRT()
				f.seededRepos = map[string]string{"root": celltest.Repo(f.cells["root"])}
				_, err = rt.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: "late-child", Name: "root", Repo: f.seededRepos["root"]})
				require.NoError(t, err)
				session, err := f.buildSession(rt, "late-child", nil)
				require.NoError(t, err)
				defer func() {
					if session.cancel != nil {
						cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
						defer stop()
						require.NoError(t, f.stopSession(cleanup, session))
					}
				}()
				parent, err := recipe.LoadInternalRecipeFromReader(strings.NewReader(parentYAML))
				require.NoError(t, err)
				root, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{Cell: "root", CellRepo: f.seededRepos["root"], TenantId: "late-child", RecipeName: "parent", GitRef: "main", JobContext: f.jobContext("root")}, session.engine, *parent)
				require.NoError(t, err)
				f.rootJobID = root.Key().JobId
				require.NoError(t, f.bindScenarioJobs(ctx, rt, "late-child"))
				require.NotContains(t, f.scenario.report.Jobs, "child")
				require.NoError(t, f.startSession(ctx, session))
				input, err := inputop.NewRuntime(f.ctl, nil)
				require.NoError(t, err)
				var childID, requestID string
				waitPending := func(previous string) {
					t.Helper()
					require.Eventually(t, func() bool {
						pending, err := input.ListPendingInputs(ctx, "late-child")
						if err != nil || len(pending) != 1 {
							return false
						}
						form, err := input.GetForm(ctx, "late-child", pending[0].Id)
						if err != nil || form.RequestID == previous {
							return false
						}
						childID, requestID = pending[0].Id, form.RequestID
						return true
					}, 10*time.Second, 10*time.Millisecond)
				}
				// Deterministically pause the controller after its binding snapshot:
				// workers publish a real child wait before any further controller pass.
				waitPending("")
				require.NoError(t, f.stopSession(ctx, session))
				require.NotContains(t, f.scenario.report.Jobs, "child")
				// Also model the publication gap: the native child exists, but its
				// binding/receipt has not yet become available to the controller.
				published := f.scenario.report.Receipts
				withhold := func() {
					if source == "binding" {
						require.NoError(t, os.Rename(filepath.Join(f.root, "child-receipts"), filepath.Join(f.root, "withheld-receipts")))
					} else {
						require.Len(t, published, 1)
						f.scenario.report.Receipts = nil
					}
				}
				restore := func() {
					if source == "binding" {
						require.NoError(t, os.Rename(filepath.Join(f.root, "withheld-receipts"), filepath.Join(f.root, "child-receipts")))
					} else {
						f.scenario.report.Receipts = published
					}
				}
				withhold()
				require.NoError(t, f.answerInputs(ctx, rt, "late-child", input))
				require.Empty(t, f.report.Reviews, "unresolved provenance must not permit a legacy reply")
				require.Empty(t, f.report.Submissions)
				require.False(t, f.responses[1])
				restore()
				// An already-bound alias is also insufficient to release the wait:
				// it can identify the very same child as the unresolved selector.
				require.NoError(t, f.bindScenarioJobs(ctx, rt, "late-child"))
				f.c.Runtime.Controls.Scenario.Jobs["known-child"] = childSource
				f.scenario.report.Jobs["known-child"] = f.scenario.report.Jobs["child"]
				delete(f.scenario.report.Jobs, "child")
				withhold()
				require.NoError(t, f.answerInputs(ctx, rt, "late-child", input))
				require.Equal(t, childID, f.scenario.report.Jobs["known-child"].Ref.JobID)
				require.NotContains(t, f.scenario.report.Jobs, "child")
				require.Empty(t, f.report.Reviews, "aliases must not bypass the unresolved child's reservation")
				require.Empty(t, f.report.Submissions)
				require.False(t, f.responses[1])
				if source == "binding" {
					// Replay the real child's creation through an alternate native
					// receipt view while its fixture binding remains withheld. The
					// actual creating call selected this binding's fixture effect;
					// a native receipt alone therefore cannot prove disjointness.
					var observed scenariotest.Submission
					for _, a := range f.report.Controls.Dispatch {
						if a.Kind == "task" && a.JobID == f.rootJobID && a.NodePath == "parent/spawn" {
							observed = scenariotest.Submission{Caller: a.JobID, InvocationID: a.InvocationID, NodePath: a.NodePath, TaskType: a.Name, Attempt: a.Attempt.Attempt, ScopeID: a.ScopeID, Index: 1, Ref: f.scenario.report.Jobs["known-child"].Ref}
							break
						}
					}
					require.NotEmpty(t, observed.InvocationID)
					f.scenario.report.Receipts = append(append([]scenariotest.Submission(nil), published...), observed)
					require.NoError(t, f.answerInputs(ctx, rt, "late-child", input))
					require.Empty(t, f.report.Reviews, "creation provenance can overlap the unpublished binding")
					require.Empty(t, f.report.Submissions)
					require.False(t, f.responses[1])
					f.scenario.report.Receipts = published
				}
				restore()
				require.NoError(t, f.answerInputs(ctx, rt, "late-child", input))
				require.Empty(t, f.report.Reviews, "legacy must not answer the unbound controlled child")
				require.False(t, f.responses[1])
				require.Len(t, f.report.Submissions, 1)
				a := f.report.Submissions[0]
				require.Equal(t, childID, a.Pending.JobID)
				require.Equal(t, requestID, a.Pending.RequestID)
				require.False(t, a.Attempted)
				require.NoError(t, f.pumpResponseSubmissions(ctx, rt, "late-child", input))
				require.True(t, f.report.Submissions[0].Complete)
				session, err = f.buildSession(rt, "late-child", nil)
				require.NoError(t, err)
				require.NoError(t, f.startSession(ctx, session))
				input, err = inputop.NewRuntime(f.ctl, nil)
				require.NoError(t, err)
				waitPending(requestID)
				require.NoError(t, f.answerInputs(ctx, rt, "late-child", input))
				require.Eventually(t, func() bool {
					info, err := f.ctl.InspectJob(ctx, jobdb.JobKey{TenantId: "late-child", JobId: childID})
					return err == nil && info.Status == "completed"
				}, 10*time.Second, 10*time.Millisecond)
				require.NoError(t, f.stopSession(ctx, session))
				require.Len(t, f.report.Reviews, 2)
				require.Equal(t, "legacy-human", f.report.Reviews[1].Receipt.Actor.ID)
				require.NotEqual(t, a.Pending.RequestID, f.report.Reviews[1].RequestID)
				continuations := 0
				for _, call := range f.report.Calls {
					if call.NodePath == "persistence/continuation" {
						continuations++
					}
				}
				require.Equal(t, 1, continuations)
			})
		}
	}
}
