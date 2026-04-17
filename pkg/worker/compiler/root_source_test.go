package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ops2 "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/stretchr/testify/require"
)

func TestRecipeSourceResolver_ResolveAndLoadGitFileSelector(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	require.NoError(t, runGit(repoDir, "git", "init"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.name", "Root Source Test"))

	recipePath := filepath.Join(repoDir, ".colony2", "recipes", "git-file.recipe.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(recipePath), 0o755))
	require.NoError(t, os.WriteFile(recipePath, []byte(strings.TrimSpace(`
id: git_file_recipe
version: "1.0"
sequence: []
outputs:
  ok: true
`)+"\n"), 0o644))
	require.NoError(t, runGit(repoDir, "git", "add", "."))
	require.NoError(t, runGit(repoDir, "git", "commit", "-m", "add recipe"))

	repoURL := (&url.URL{Scheme: "file", Path: repoDir}).String()
	selector := fmt.Sprintf("git+%s//.colony2/recipes/git-file.recipe.yaml@HEAD", repoURL)

	resolver := NewRecipeSourceResolver(RecipeSourceResolverOptions{})
	resolution, err := resolver.Resolve(context.Background(), "tenant", selector)
	require.NoError(t, err)
	require.Equal(t, RecipeSourceKindGit, resolution.SourceKind)
	require.Equal(t, selector, resolution.SubmittedSelector)
	require.Len(t, resolution.ResolvedCommit, 40)
	require.Contains(t, resolution.ResolvedSelector, "@"+resolution.ResolvedCommit)
	require.False(t, resolution.WasAlreadyPinned)

	rec, err := resolver.Load(context.Background(), "tenant", resolution)
	require.NoError(t, err)
	require.Equal(t, "git_file_recipe", strings.TrimSpace(rec.GetMetadata().ID))
}

func TestRecipeJobWorker_ResolvesRootRecipeAtExecutionTime(t *testing.T) {
	t.Parallel()

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)

	const opType = "root_source_test_echo"
	type echoInput struct {
		Value string `json:"value"`
	}
	type echoOutput struct {
		Value string `json:"value"`
	}
	op := ops2.NewActivityMappedOpV2[echoInput, echoOutput](ops2.OpMetadata{Type: opType}, func(_ ops2.OpDependencies, _ context.Context, input echoInput) (echoOutput, error) {
		return echoOutput{Value: input.Value}, nil
	})
	require.NoError(t, workerops.Register(registry, op))
	ops2.Register(op)

	rec, err := recipe.LoadRecipeFromString([]byte(strings.TrimSpace(`
id: remote_root_recipe
version: "1.0"
sequence:
  - id: echo
    op: root_source_test_echo
    inputs:
      value: resolved-at-runtime
outputs:
  result: "{{ sequence.echo.outputs.value }}"
`)))
	require.NoError(t, err)

	resolver := NewRecipeSourceResolver(RecipeSourceResolverOptions{
		RecipeRefResolver: NewProviderBackedRecipeRefResolver(func(projectID string, recipeRef string) (*recipe.Recipe, error) {
			require.Equal(t, "tenant", projectID)
			require.Equal(t, "remote-root", recipeRef)
			return rec, nil
		}),
	})

	workset, err := NewRecipeWorkerWithOptions(ops2.NewServiceDepsBuilder().Build(), registry, RecipeJobWorkerOptions{
		RootSourceResolver: resolver,
	})
	require.NoError(t, err)

	engine := newToyEngineWithWorkSet(t, workset, nil)
	jobCtx, gitCtx := GenerateTestContext()
	job := workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: "remote-root",
		Inputs:     map[string]interface{}{},
		JobContext: jobCtx,
		GitRef:     gitCtx.ParentRef,
	}

	jobKey, err := starter.StartRecipeJob(context.Background(), job, engine)
	require.NoError(t, err)
	require.NoError(t, swf.WaitForJobToComplete(context.Background(), 30*time.Second, jobKey, engine))

	out, err := swfutil.JobResult(context.Background(), engine, jobKey)
	require.NoError(t, err)
	raw, err := out.GetData()
	require.NoError(t, err)
	got := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "resolved-at-runtime", got["result"])

	run, err := engine.GetJobRun(context.Background(), swf.GetJobRunRequest{
		JobKey:         jobKey,
		IncludeOutputs: true,
	})
	require.NoError(t, err)
	require.Len(t, run.Attempts, 1)
	require.GreaterOrEqual(t, len(run.Attempts[0].Tasks), 2)
	require.Equal(t, RootSourceResolutionTaskType, run.Attempts[0].Tasks[0].TaskType)
	require.NotEmpty(t, run.Attempts[0].Tasks[0].Attempts)
	require.NotNil(t, run.Attempts[0].Tasks[0].Attempts[0].Output)
	resolvedSource, err := ParseResolvedRecipeSourceJSON(run.Attempts[0].Tasks[0].Attempts[0].Output.Data)
	require.NoError(t, err)
	require.Equal(t, "remote-root", resolvedSource.SubmittedSelector)
	require.Contains(t, resolvedSource.RecipeYAML, "id: remote_root_recipe")
}
