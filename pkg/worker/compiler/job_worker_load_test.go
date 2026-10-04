package compiler

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type loadFailureContext struct {
	jobworkflow.JobContext
	output jobdb.TaskData
	err    error
	calls  int
}

func (c *loadFailureContext) GetJobKey() jobdb.JobKey {
	return jobdb.JobKey{TenantId: "tenant", JobId: "job"}
}
func (c *loadFailureContext) Logger() *slog.Logger { return slog.Default() }
func (c *loadFailureContext) DoTask(_ jobdb.RunPolicy, kind string, _ jobdb.TaskData) (jobdb.TaskData, error) {
	if kind != RootSourceResolutionTaskType {
		panic("execution continued after recipe load failure")
	}
	c.calls++
	return c.output, c.err
}

type failingRecipeLoader struct {
	err   error
	calls int
}

func (l *failingRecipeLoader) Resolve(context.Context, string, string) (RecipeSourceResolution, error) {
	panic("replay must use recorded resolution")
}
func (l *failingRecipeLoader) Load(context.Context, string, RecipeSourceResolution) (recipe.Recipe, error) {
	l.calls++
	return recipe.Recipe{}, l.err
}

func TestRecipeJobWorkerLoadFailuresStopBeforeExecution(t *testing.T) {
	loaderErr := errors.New("recipe source unavailable")
	sourceErr := errors.New("cannot read resolution chapter")
	for _, tc := range []struct {
		name, yaml, want string
		embedded         bool
		loadErr, taskErr error
		malformedOutput  bool
	}{
		{name: "resolved invalid yaml", yaml: "sequence: [", want: "parse resolved recipe YAML"},
		{name: "resolved unknown nested op", yaml: "id: test\nsequence:\n - sequence:\n    - op: deliberately_unregistered_load_test\n", want: "unknown op"},
		{name: "legacy loader failure", loadErr: loaderErr, want: loaderErr.Error()},
		{name: "legacy empty recipe", want: "empty recipe"},
		{name: "embedded invalid yaml", embedded: true, yaml: "sequence: [", want: "unmarshal YAML"},
		{name: "embedded unknown op", embedded: true, yaml: "id: test\nop: deliberately_unregistered_load_test\n", want: "unknown op"},
		{name: "resolution read failure", taskErr: sourceErr, want: sourceErr.Error()},
		{name: "malformed resolution", malformedOutput: true, want: "decode resolved recipe source payload"},
	} {
		for _, replay := range []bool{false, true} {
			mode := "execution"
			if replay {
				mode = "replay"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				loader := &failingRecipeLoader{err: tc.loadErr}
				source, err := jobdb.NewTaskData(ResolvedRecipeSource{RecipeSourceResolution: RecipeSourceResolution{SourceKind: RecipeSourceKindGit, SubmittedSelector: "test"}, RecipeYAML: tc.yaml})
				require.NoError(t, err)
				if tc.malformedOutput {
					source = &jobdb.SimpleTaskData{Data: []byte(`{"broken":true}`)}
				}
				ctx := &loadFailureContext{output: source, err: tc.taskErr}
				var artifacts []jobdb.Artifact
				if tc.embedded {
					artifacts = append(artifacts, jobdb.NewArtifactFromBytes("test"+starter.RecipeArtifactSuffix, []byte(tc.yaml)))
				}
				start, err := jobdb.NewTaskData(workflowctl.StartJob{RecipeName: "test", TenantId: "tenant"}, artifacts...)
				require.NoError(t, err)
				worker := NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: replay, RootSourceResolver: loader, OnRecipeSourceResolved: func(RecipeSourceResolution) { t.Fatal("reported an unloaded recipe as resolved") }, ExecutorFactory: func() RecipeExecutor { t.Fatal("created executor after failed load"); return nil }})
				require.NotPanics(t, func() { _, err = worker.Run(ctx, start) })
				require.ErrorContains(t, err, tc.want)
				if tc.loadErr != nil {
					require.ErrorIs(t, err, tc.loadErr)
				}
				if tc.taskErr != nil {
					require.ErrorIs(t, err, tc.taskErr)
				}
				if tc.embedded {
					require.Zero(t, ctx.calls)
				} else {
					require.Equal(t, 1, ctx.calls)
				}
			})
		}
	}
}
