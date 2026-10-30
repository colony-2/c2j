package compiler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// A prior output that records serialization lets this test catch discarded
// snapshots too, including a clone used only by a task input builder.
type snapshotWitness struct{ copies int }

func (w *snapshotWitness) MarshalJSON() ([]byte, error) {
	w.copies++
	return []byte(`"prior output"`), nil
}

func TestSerialScopesAndInputBuildersDoNotSnapshotPriorOutputs(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	rt := toy.New()
	job, git := GenerateTestContext()
	task := &scopeProbeTask{calls: map[string]int{}, git: git}
	w := jobworkflow.ExecutionWorker{JobType: "no-snapshots", Execute: func(exec jobworkflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		root, err := template.NewRecipeResolutionContext(&git, nil, job)
		if err != nil {
			return nil, err
		}
		parent, err := root.NewChildContext(template.ScopeSequence, recipe.NodeMetadata{ID: "container"}, "", map[string]any{})
		if err != nil {
			return nil, err
		}
		prior, err := parent.NewChildContext(template.ScopeOp, recipe.NodeMetadata{ID: "prior"}, "", nil)
		if err != nil {
			return nil, err
		}
		witness := &snapshotWitness{}
		if err = prior.AddExecution(map[string]any{"value": witness}); err != nil {
			return nil, err
		}
		for _, id := range []string{"a", "b"} {
			node := probeNode(id)
			if err = (DefaultRecipeExecutor{}).ExecuteNode(catchWorkflowContext(exec), parent, &node); err != nil {
				return nil, err
			}
		}
		if witness.copies != 0 || parent.TemplateData.Sequence["prior"].Outputs["value"] != witness {
			return nil, fmt.Errorf("ordinary execution copied prior output (%d serializations)", witness.copies)
		}
		return jobdb.NewTaskData(nil)
	}}
	key := probeSubmit(t, rt, w)
	out := probeRun(t, rt, key, w, task)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
	require.Nil(t, out.JobError)
	require.NoError(t, probeReplay(t, rt, key, w))
	require.Equal(t, map[string]int{"a": 1, "b": 1}, task.calls)
}

// This boundary runs after the body produces its result, before JobDB accepts
// it. It works with either synchronous or asynchronous scope invocation.
type scopeDecisionProbe struct {
	jobworkflow.ExecutionContext
	decide func(string) error
}

func (c scopeDecisionProbe) DoScope(site string, opts jobworkflow.ScopeOptions, body func(jobworkflow.ExecutionContext) (jobdb.JobData, error)) (jobdb.JobData, error) {
	return c.ExecutionContext.DoScope(site, opts, func(inner jobworkflow.ExecutionContext) (jobdb.JobData, error) {
		out, err := body(scopeDecisionProbe{inner, c.decide})
		if err == nil {
			err = c.decide(site)
		}
		return out, err
	})
}

func TestScopeOutputPublishedOnlyAfterAcceptance(t *testing.T) {
	for _, kind := range []template.ScopeType{template.ScopeOp, template.ScopeSequence, template.ScopeStateMachine, template.ScopeState} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reject=%v", kind, reject), func(t *testing.T) {
				rt := toy.New()
				job, git := GenerateTestContext()
				var observedErr error
				var before, after map[string]template.StepOutput
				w := jobworkflow.ExecutionWorker{JobType: "publication-boundary", Execute: func(exec jobworkflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
					root, err := template.NewRecipeResolutionContext(&git, nil, job)
					if err != nil {
						return nil, err
					}
					containerKind := template.ScopeSequence
					if kind == template.ScopeState {
						containerKind = template.ScopeStateMachine
					}
					parent, err := root.NewChildContext(containerKind, recipe.NodeMetadata{ID: "container"}, "", map[string]any{})
					if err != nil {
						return nil, err
					}
					view := func(r *template.ResolutionContext) map[string]template.StepOutput {
						if kind == template.ScopeState {
							return r.TemplateData.States
						}
						return r.TemplateData.Sequence
					}
					ctx := catchWorkflowContext(scopeDecisionProbe{exec, func(string) error {
						if len(before) != 0 {
							return fmt.Errorf("output was visible before scope acceptance")
						}
						if reject {
							return fmt.Errorf("result rejected at scope boundary")
						}
						return nil
					}})
					meta := recipe.NodeMetadata{ID: "work"}
					observedErr = executeNodeScope(ctx, parent, meta, kind, "", func(inner workflow.Context, entry *template.ResolutionContext, meta recipe.NodeMetadata) error {
						var inputs map[string]any
						if kind == template.ScopeSequence || kind == template.ScopeStateMachine {
							inputs = map[string]any{}
						}
						scope, err := entry.NewChildContext(kind, meta, "", inputs)
						if err != nil {
							return err
						}
						before = view(entry)
						return completeNodeScope(inner, scope, template.ExecutionOutput{Outputs: map[string]any{"value": "accepted"}})
					})
					after = view(parent)
					return jobdb.NewTaskData(nil)
				}}
				key := probeSubmit(t, rt, w)
				require.Equal(t, jobworkflow.JobRunCompleted, probeRun(t, rt, key, w).Status)
				if reject {
					require.ErrorContains(t, observedErr, "result rejected at scope boundary")
					require.Empty(t, after)
				} else {
					require.NoError(t, observedErr)
					require.Equal(t, "accepted", after["work"].Outputs["value"])
					require.Empty(t, after["work"].Runs)
				}
			})
		}
	}
}

func TestStateOwnsNestedOpPublication(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprintf("reject=%v", reject), func(t *testing.T) {
			rt := toy.New()
			job, git := GenerateTestContext()
			var observedErr error
			var after map[string]template.StepOutput
			w := jobworkflow.ExecutionWorker{JobType: "state-publication", Execute: func(exec jobworkflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
				root, err := template.NewRecipeResolutionContext(&git, nil, job)
				if err != nil {
					return nil, err
				}
				machine, err := root.NewChildContext(template.ScopeStateMachine, recipe.NodeMetadata{ID: "machine"}, "", map[string]any{})
				if err != nil {
					return nil, err
				}
				ctx := catchWorkflowContext(scopeDecisionProbe{exec, func(site string) error {
					if reject && strings.HasPrefix(site, "state:") {
						return fmt.Errorf("state result rejected")
					}
					return nil
				}})
				meta := recipe.NodeMetadata{ID: "work"}
				observedErr = executeNodeScope(ctx, machine, meta, template.ScopeState, "", func(inner workflow.Context, entry *template.ResolutionContext, meta recipe.NodeMetadata) error {
					state, err := entry.NewChildContext(template.ScopeState, meta, "", nil)
					if err != nil {
						return err
					}
					err = executeNodeScope(inner, state, recipe.NodeMetadata{ID: "op"}, template.ScopeOp, "", func(inner workflow.Context, parent *template.ResolutionContext, meta recipe.NodeMetadata) error {
						op, err := parent.NewChildContext(template.ScopeOp, meta, "", nil)
						if err != nil {
							return err
						}
						return completeNodeScope(inner, op, template.ExecutionOutput{Outputs: map[string]any{"value": "accepted"}})
					})
					if err == nil && len(state.TemplateData.States) != 0 {
						return fmt.Errorf("accepted op published before its owning state completed")
					}
					return err
				})
				after = machine.TemplateData.States
				return jobdb.NewTaskData(nil)
			}}
			key := probeSubmit(t, rt, w)
			require.Equal(t, jobworkflow.JobRunCompleted, probeRun(t, rt, key, w).Status)
			if reject {
				require.ErrorContains(t, observedErr, "state result rejected")
				require.Empty(t, after)
			} else {
				require.NoError(t, observedErr)
				require.Equal(t, "accepted", after["work"].Outputs["value"])
				require.Empty(t, after["work"].Runs)
			}
		})
	}
}
