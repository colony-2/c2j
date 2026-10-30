package compiler

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type lateCompletionTask struct {
	release  <-chan struct{}
	finished chan<- struct{}
	git      contextual.GitCommitContext
}

func (lateCompletionTask) Name() string { return "late_publication_probe:run" }
func (task lateCompletionTask) Run(ctx jobworkflow.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	<-ctx.Context().Done()
	<-task.release
	defer close(task.finished)
	return scopeProbeOutput(map[string]any{"value": "late"}, task.git)
}

// Tasks may outlive cancellation. They return data to JobDB and never receive a
// resolution context; only the scope caller applies an accepted task result.
func TestTaskLateCompletionCannotReplaceHandledOutput(t *testing.T) {
	registerCatchOp(t, "late_publication_probe")
	handled := recipe.NodeMetadata{ID: "work", Catch: []recipe.CatchClause{{
		When:     catchExpr(t, "true"),
		Continue: &recipe.CatchContinue{Outputs: map[string]any{"value": "handled"}},
	}}}
	synctest.Test(t, func(t *testing.T) {
		rt := toy.New()
		release := make(chan struct{})
		finished := make(chan struct{})
		job, git := GenerateTestContext()
		initialGit := git
		lateGit := git
		lateGit.PersistHash = "late-task-commit"
		type observation struct {
			before, after any
			git           contextual.GitCommitContext
			runs          []template.RunOutput
		}
		observed := make(chan observation, 1)
		w := jobworkflow.ExecutionWorker{
			JobType: "late-publication-probe",
			Execute: func(exec jobworkflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
				root, err := template.NewRecipeResolutionContext(&git, nil, job)
				if err != nil {
					return nil, err
				}
				machine, err := root.NewChildContext(template.ScopeStateMachine, recipe.NodeMetadata{ID: "machine"}, "", map[string]any{})
				if err != nil {
					return nil, err
				}
				err = executeNodeScope(catchWorkflowContext(exec), machine, handled, template.ScopeState, "work", func(inner workflow.Context, parent *template.ResolutionContext, clean recipe.NodeMetadata) error {
					state, err := parent.NewChildContext(template.ScopeState, clean, "work", nil)
					if err != nil {
						return err
					}
					if inner.ScopeState != nil {
						inner.ScopeState(state)
					}
					return (DefaultRecipeExecutor{}).ExecuteOp(inner, state, recipe.NodeMetadata{ID: "body", Timeout: recipe.Duration(time.Second)}, "late_publication_probe")
				})
				if err != nil {
					close(release)
					<-finished
					return nil, err
				}
				before := machine.TemplateData.States["work"].Outputs["value"]
				close(release)
				<-finished
				after := machine.TemplateData.States["work"].Outputs["value"]
				observed <- observation{before, after, root.GetGitCommitContext(), machine.TemplateData.States["work"].Runs}
				return jobdb.NewTaskData(nil)
			},
		}
		key := probeSubmit(t, rt, w)
		out := probeRun(t, rt, key, w, lateCompletionTask{release, finished, lateGit})
		require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
		values := <-observed
		require.Equal(t, "handled", values.before)
		require.Equal(t, "handled", values.after)
		require.Empty(t, values.runs, "discarded task results must not enter history")
		require.Equal(t, initialGit, values.git, "discarded task results must not advance Git bookkeeping")
	})
}
