package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/git/selectorcache"
	ops2 "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type renameAfterWithinResolutionContext struct {
	jobKey  jobdb.JobKey
	repoDir string
	moved   bool
}

func (c *renameAfterWithinResolutionContext) AwaitJobs(jobIds ...string) error   { return nil }
func (c *renameAfterWithinResolutionContext) GetJobKey() jobdb.JobKey            { return c.jobKey }
func (c *renameAfterWithinResolutionContext) Logger() *slog.Logger               { return slog.Default() }
func (c *renameAfterWithinResolutionContext) AwaitDuration(jobdb.Duration) error { return nil }

func (c *renameAfterWithinResolutionContext) DoTask(_ jobdb.RunPolicy, taskType string, data jobdb.TaskData) (jobdb.TaskData, error) {
	if taskType != WithinRecipeResolutionTaskType {
		return nil, fmt.Errorf("unexpected task %q before validation wrapper", taskType)
	}
	out, err := newWithinRecipeResolutionTaskWorker().Run(jobworkflow.TaskContext{}, data)
	if err != nil {
		return nil, err
	}
	if !c.moved {
		c.moved = true
		if err := os.Rename(c.repoDir, c.repoDir+".gone"); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func ensureExtensionExecutionRegistered(t *testing.T, registry *workerops.ActivityRegistry) {
	t.Helper()

	extensionExecution := extops.GetExecutionOp()
	if _, exists := ops2.Get(extops.ExecutionOpType); !exists {
		ops2.Register(extensionExecution)
	}
	if _, exists := registry.Get(extops.ExecutionOpType + ":" + extops.ExecutionOpType); !exists {
		require.NoError(t, workerops.Register(registry, extensionExecution))
	}
}

func TestSelectorRepoRefKeyForLocalSelectorUsesRecipeRepoRef(t *testing.T) {
	key, ok, err := selectorRepoRefKey("./tools/ops/echo", "github.com/acme/repo", "main")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, selectorcache.RepoRefKey("https://github.com/acme/repo.git", "main"), key)

	key, ok, err = selectorRepoRefKey("./tools/ops/echo", "", "")
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, key)
}

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

func TestRecipeSourceResolver_GitSelectorUsesSharedCacheForPinnedOfflineLoad(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	require.NoError(t, runGit(repoDir, "git", "init"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.name", "Root Source Test"))

	recipePath := filepath.Join(repoDir, ".c2j", "recipes", "cached.yaml")
	writeRootSourceRecipe(t, recipePath, "cached_recipe")
	require.NoError(t, runGit(repoDir, "git", "add", "."))
	require.NoError(t, runGit(repoDir, "git", "commit", "-m", "add cached recipe"))

	repoURL := (&url.URL{Scheme: "file", Path: repoDir}).String()
	selector := fmt.Sprintf("git+%s//.c2j/recipes/cached.yaml@HEAD", repoURL)
	cache := &selectorcache.Cache{Root: t.TempDir()}
	resolver := NewRecipeSourceResolver(RecipeSourceResolverOptions{SelectorCache: cache})

	resolution, err := resolver.Resolve(context.Background(), "tenant", selector)
	require.NoError(t, err)
	require.Len(t, resolution.ResolvedCommit, 40)
	require.Equal(t, fmt.Sprintf("git+%s//.c2j/recipes/cached.yaml@%s", repoURL, resolution.ResolvedCommit), resolution.ResolvedSelector)

	// The source tree has been materialized once. Loading the pinned resolution
	// should not need the remote repository anymore.
	require.NoError(t, os.Rename(repoDir, repoDir+".gone"))
	rec, err := resolver.Load(context.Background(), "tenant", resolution)
	require.NoError(t, err)
	require.Equal(t, "cached_recipe", strings.TrimSpace(rec.GetMetadata().ID))

	pinnedResolution, err := resolver.Resolve(context.Background(), "tenant", resolution.ResolvedSelector)
	require.NoError(t, err)
	require.True(t, pinnedResolution.WasAlreadyPinned)
	require.Equal(t, resolution.ResolvedCommit, pinnedResolution.ResolvedCommit)
}

func TestRecipeSourceResolver_MutableGitSelectorTracksMovedBranchInCache(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	require.NoError(t, runGit(repoDir, "git", "init"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.name", "Root Source Test"))

	recipePath := filepath.Join(repoDir, ".c2j", "recipes", "moving.yaml")
	writeRootSourceRecipe(t, recipePath, "moving_recipe_v1")
	require.NoError(t, runGit(repoDir, "git", "add", "."))
	require.NoError(t, runGit(repoDir, "git", "commit", "-m", "add moving recipe v1"))
	firstCommit := gitHead(t, repoDir)

	repoURL := (&url.URL{Scheme: "file", Path: repoDir}).String()
	selector := fmt.Sprintf("git+%s//.c2j/recipes/moving.yaml@HEAD", repoURL)
	cache := &selectorcache.Cache{Root: t.TempDir()}
	resolver := NewRecipeSourceResolver(RecipeSourceResolverOptions{SelectorCache: cache})

	firstResolution, err := resolver.Resolve(context.Background(), "tenant", selector)
	require.NoError(t, err)
	require.Equal(t, firstCommit, firstResolution.ResolvedCommit)
	firstRecipe, err := resolver.Load(context.Background(), "tenant", firstResolution)
	require.NoError(t, err)
	require.Equal(t, "moving_recipe_v1", strings.TrimSpace(firstRecipe.GetMetadata().ID))

	writeRootSourceRecipe(t, recipePath, "moving_recipe_v2")
	require.NoError(t, runGit(repoDir, "git", "add", "."))
	require.NoError(t, runGit(repoDir, "git", "commit", "-m", "add moving recipe v2"))
	secondCommit := gitHead(t, repoDir)
	require.NotEqual(t, firstCommit, secondCommit)

	secondResolution, err := resolver.Resolve(context.Background(), "tenant", selector)
	require.NoError(t, err)
	require.Equal(t, secondCommit, secondResolution.ResolvedCommit)
	secondRecipe, err := resolver.Load(context.Background(), "tenant", secondResolution)
	require.NoError(t, err)
	require.Equal(t, "moving_recipe_v2", strings.TrimSpace(secondRecipe.GetMetadata().ID))

	require.NotEqual(t, firstResolution.ResolvedSelector, secondResolution.ResolvedSelector)
	require.Len(t, cachedCommitDirs(t, cache.Root), 2)
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

	engine := newToyEngineWithWorkSet(t, "tenant", workset, nil)
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
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 30*time.Second, jobKey, engine))

	out, err := swfutil.JobResult(context.Background(), engine, jobKey)
	require.NoError(t, err)
	raw, err := out.GetData()
	require.NoError(t, err)
	got := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "resolved-at-runtime", got["result"])

	run, err := engine.GetJobRun(context.Background(), jobdb.GetJobRunRequest{
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

func TestRecipeJobWorker_ResolvesBareRootRecipeAgainstLookupContext(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	require.NoError(t, runGit(repoDir, "git", "init"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.name", "Root Source Test"))

	recipePath := filepath.Join(repoDir, ".c2j", "recipes", "remote-root.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(recipePath), 0o755))
	require.NoError(t, os.WriteFile(recipePath, []byte(strings.TrimSpace(`
id: remote_root_lookup_recipe
version: "1.0"
sequence: []
outputs:
  ok: true
`)+"\n"), 0o644))
	require.NoError(t, runGit(repoDir, "git", "add", "."))
	require.NoError(t, runGit(repoDir, "git", "commit", "-m", "add bare lookup recipe"))

	output, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, string(output))
	commit := strings.TrimSpace(string(output))
	repoURL := (&url.URL{Scheme: "file", Path: repoDir}).String()

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)

	workset, err := NewRecipeWorkerWithOptions(ops2.NewServiceDepsBuilder().Build(), registry, RecipeJobWorkerOptions{
		RootSourceResolver: NewRecipeSourceResolver(RecipeSourceResolverOptions{}),
	})
	require.NoError(t, err)

	engine := newToyEngineWithWorkSet(t, "tenant", workset, nil)
	jobCtx, _ := GenerateTestContext()
	jobCtx.RecipeSource.Repo = repoURL
	jobCtx.RecipeSource.Ref = commit
	job := workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: "remote-root",
		Inputs:     map[string]interface{}{},
		JobContext: jobCtx,
		GitRef:     commit,
	}

	jobKey, err := starter.StartRecipeJob(context.Background(), job, engine)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 30*time.Second, jobKey, engine))

	out, err := swfutil.JobResult(context.Background(), engine, jobKey)
	require.NoError(t, err)
	raw, err := out.GetData()
	require.NoError(t, err)
	got := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, true, got["ok"])

	run, err := engine.GetJobRun(context.Background(), jobdb.GetJobRunRequest{
		JobKey:         jobKey,
		IncludeOutputs: true,
	})
	require.NoError(t, err)
	require.Len(t, run.Attempts, 1)
	require.GreaterOrEqual(t, len(run.Attempts[0].Tasks), 1)
	require.Equal(t, RootSourceResolutionTaskType, run.Attempts[0].Tasks[0].TaskType)

	resolvedSource, err := ParseResolvedRecipeSourceJSON(run.Attempts[0].Tasks[0].Attempts[0].Output.Data)
	require.NoError(t, err)
	require.Equal(t, "remote-root", resolvedSource.SubmittedSelector)
	require.Equal(t, fmt.Sprintf("git+%s//.c2j/recipes/remote-root.yaml@%s", repoURL, commit), resolvedSource.ResolvedSelector)
}

func TestWithinRecipeResolutionTaskResolvesSelectors(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	require.NoError(t, runGit(repoDir, "git", "init"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.name", "Root Source Test"))

	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "tools", "ops", "echo"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "tools", "cel", "text-utils"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "tools", "ops", "echo", "op.yaml"), []byte("name: echo\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "tools", "cel", "text-utils", "functions.yaml"), []byte("name: text_utils\nfunctions:\n  - name: slugify\n    mode: function\n    execution: cat\n    args:\n      - name: input\n        schema:\n          type: string\n    returns:\n      schema:\n        type: string\n"), 0o644))
	require.NoError(t, runGit(repoDir, "git", "add", "."))
	require.NoError(t, runGit(repoDir, "git", "commit", "-m", "add selector targets"))

	repoURL := (&url.URL{Scheme: "file", Path: repoDir}).String()
	output, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, string(output))
	commit := strings.TrimSpace(string(output))
	worker := newWithinRecipeResolutionTaskWorker()

	input, err := jobdb.NewTaskData(withinRecipeResolutionTaskInput{
		Selectors: []string{
			fmt.Sprintf("git+%s//tools/ops/echo@HEAD", repoURL),
			fmt.Sprintf("git+%s//tools/cel/text-utils@HEAD", repoURL),
		},
	})
	require.NoError(t, err)

	out, err := worker.Run(jobworkflow.TaskContext{}, input)
	require.NoError(t, err)

	resolved, err := ParseWithinRecipeResolutionTaskData(out)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("git+%s//tools/ops/echo@%s", repoURL, commit), resolved.ResolvedSelectors[fmt.Sprintf("git+%s//tools/ops/echo@HEAD", repoURL)])
	require.Equal(t, fmt.Sprintf("git+%s//tools/cel/text-utils@%s", repoURL, commit), resolved.ResolvedSelectors[fmt.Sprintf("git+%s//tools/cel/text-utils@HEAD", repoURL)])
	require.Equal(t, map[string]string{
		selectorcache.RepoRefKey(repoURL, "HEAD"): commit,
	}, resolved.ResolvedGitRefs)
}

func TestWithinRecipeResolutionUsesRepoRefPinAcrossDifferentSelectorPaths(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	require.NoError(t, runGit(repoDir, "git", "init"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.name", "Root Source Test"))

	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "tools", "ops", "first"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "tools", "ops", "second"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "tools", "ops", "first", "op.yaml"), []byte("name: first\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "tools", "ops", "second", "op.yaml"), []byte("name: second\n"), 0o644))
	require.NoError(t, runGit(repoDir, "git", "add", "."))
	require.NoError(t, runGit(repoDir, "git", "commit", "-m", "add selector targets"))
	commit := gitHead(t, repoDir)
	repoURL := (&url.URL{Scheme: "file", Path: repoDir}).String()

	firstSelector := fmt.Sprintf("git+%s//tools/ops/first@HEAD", repoURL)
	secondSelector := fmt.Sprintf("git+%s//tools/ops/second@HEAD", repoURL)
	first, err := resolveSelectors(context.Background(), []string{firstSelector}, extops.ResolveOptions{})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		selectorcache.RepoRefKey(repoURL, "HEAD"): commit,
	}, first.ResolvedGitRefs)

	require.NoError(t, os.Rename(repoDir, repoDir+".gone"))
	second, err := resolveSelectors(context.Background(), []string{secondSelector}, extops.ResolveOptions{
		ResolvedRefs: first.ResolvedGitRefs,
	})
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("git+%s//tools/ops/second@%s", repoURL, commit), second.ResolvedSelectors[secondSelector])
	require.Equal(t, first.ResolvedGitRefs, second.ResolvedGitRefs)
}

func TestValidateSelectorOpUsesSnapshotPinAfterWithinResolution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, exists := ops2.Get(extops.ExecutionOpType); !exists {
		ops2.Register(extops.GetExecutionOp())
	}

	repoDir := t.TempDir()
	require.NoError(t, runGit(repoDir, "git", "init"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.name", "Root Source Test"))

	opDir := filepath.Join(repoDir, "tools", "ops", "echo")
	require.NoError(t, os.MkdirAll(opDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(`
name: echo
shell: sh
run: cat
input_schema:
  type: object
  properties: {}
output_schema:
  type: object
  properties:
    message:
      type: string
`), 0o644))
	require.NoError(t, runGit(repoDir, "git", "add", "."))
	require.NoError(t, runGit(repoDir, "git", "commit", "-m", "add selector target"))
	repoURL := (&url.URL{Scheme: "file", Path: repoDir}).String()

	rec, err := recipe.LoadRecipeFromString([]byte(strings.TrimSpace(fmt.Sprintf(`
id: validate_selector_snapshot_pin
version: "1.0"
sequence:
  - id: echo
    op: git+%s//tools/ops/echo@HEAD
    inputs: {}
outputs:
  message: "${{ sequence.echo.outputs.message }}"
`, repoURL)) + "\n"))
	require.NoError(t, err)

	jobCtx, gitCtx := GenerateTestContext()
	inner := &renameAfterWithinResolutionContext{
		jobKey:  jobdb.JobKey{TenantId: "tenant", JobId: "job"},
		repoDir: repoDir,
	}

	result, _, err := ExecuteRecipe(newWorkflowContext(inner), *rec, nil, jobCtx, gitCtx, ExecutionOptions{Mode: ExecutionModeValidate})
	require.NoError(t, err)
	require.True(t, inner.moved, "test did not move the remote after within-recipe resolution")
	require.Equal(t, map[string]interface{}{"message": ""}, result)
}

func TestRecipeJobWorker_EmbeddedRecipeSkipsRootSourceResolutionTask(t *testing.T) {
	t.Parallel()

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)

	const opType = "embedded_root_source_test_echo"
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
id: embedded_root_recipe
version: "1.0"
sequence:
  - id: echo
    op: embedded_root_source_test_echo
    inputs:
      value: from-embedded
outputs:
  result: "{{ sequence.echo.outputs.value }}"
`)))
	require.NoError(t, err)

	workset, err := NewRecipeWorkerWithOptions(ops2.NewServiceDepsBuilder().Build(), registry, RecipeJobWorkerOptions{})
	require.NoError(t, err)

	engine := newToyEngineWithWorkSet(t, "tenant", workset, nil)
	jobCtx, gitCtx := GenerateTestContext()
	job := workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: rec.GetMetadata().ID,
		Inputs:     map[string]interface{}{},
		JobContext: jobCtx,
		GitRef:     gitCtx.ParentRef,
	}

	jobKey, err := starter.StartRecipeJob(context.Background(), job, engine, *rec)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 30*time.Second, jobKey, engine))

	out, err := swfutil.JobResult(context.Background(), engine, jobKey)
	require.NoError(t, err)
	raw, err := out.GetData()
	require.NoError(t, err)
	got := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "from-embedded", got["result"])

	run, err := engine.GetJobRun(context.Background(), jobdb.GetJobRunRequest{
		JobKey:         jobKey,
		IncludeOutputs: true,
	})
	require.NoError(t, err)
	require.Len(t, run.Attempts, 1)
	for _, task := range run.Attempts[0].Tasks {
		require.NotEqual(t, RootSourceResolutionTaskType, task.TaskType)
	}
}

func TestRecipeJobWorker_EmbeddedRecipeUsesCurrentCellRefsWithoutWithinRecipeResolution(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	require.NoError(t, runGit(repoDir, "git", "init"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.name", "Root Source Test"))

	opDir := filepath.Join(repoDir, "tools", "ops", "echo")
	funcDir := filepath.Join(repoDir, "tools", "cel", "text-utils")
	require.NoError(t, os.MkdirAll(opDir, 0o755))
	require.NoError(t, os.MkdirAll(funcDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(`
name: echo
shell: bash
run: cat
input_schema:
  type: object
  required: [message]
  properties:
    message:
      type: string
output_schema:
  type: object
  properties:
    message:
      type: string
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(funcDir, "functions.yaml"), []byte(`name: text_utils
functions:
  - name: slugify
    mode: function
    execution: sh slugify.sh
    args:
      - name: input
        schema:
          type: string
    returns:
      schema:
        type: string
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(funcDir, "slugify.sh"), []byte(`#!/bin/sh
input=$(cat)
printf '%s' "$input" | tr '[:upper:]' '[:lower:]' | tr ' ' '-'
`), 0o755))
	require.NoError(t, runGit(repoDir, "git", "add", "."))
	require.NoError(t, runGit(repoDir, "git", "commit", "-m", "add embedded selector packages"))

	output, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, string(output))
	commit := strings.TrimSpace(string(output))
	repoURL := (&url.URL{Scheme: "file", Path: repoDir}).String()

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ensureExtensionExecutionRegistered(t, registry)

	rec, err := recipe.LoadRecipeFromString([]byte(strings.TrimSpace(`
id: embedded_selector_runtime
version: "1.0"
input_schema:
  title:
    type: string
    required: true
inputs:
  title: "${{ inputs.title }}"
extensions:
  functions:
    - selector: ./tools/cel/text-utils
sequence:
  - id: echo
    op: ./tools/ops/echo
    inputs:
      message: "${{ inputs.title }}"
outputs:
  slug: "${{ slugify(sequence.echo.outputs.message) }}"
`)))
	require.NoError(t, err)

	workset, err := NewRecipeWorkerWithOptions(ops2.NewServiceDepsBuilder().Build(), registry, RecipeJobWorkerOptions{})
	require.NoError(t, err)

	engine := newToyEngineWithWorkSet(t, "tenant", workset, nil)
	jobCtx, _ := GenerateTestContext()
	jobCtx.GitBase.BaseRepo = repoURL
	jobCtx.GitBase.BaseRef = commit
	jobCtx.GitBase.ResolvedBaseHash = commit
	job := workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: rec.GetMetadata().ID,
		Inputs: map[string]interface{}{
			"title": "Hello World",
		},
		JobContext: jobCtx,
		GitRef:     commit,
	}

	jobKey, err := starter.StartRecipeJob(context.Background(), job, engine, *rec)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 30*time.Second, jobKey, engine))

	out, err := swfutil.JobResult(context.Background(), engine, jobKey)
	require.NoError(t, err)
	raw, err := out.GetData()
	require.NoError(t, err)
	got := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "hello-world", got["slug"])

	run, err := engine.GetJobRun(context.Background(), jobdb.GetJobRunRequest{
		JobKey:         jobKey,
		IncludeOutputs: true,
	})
	require.NoError(t, err)
	require.Len(t, run.Attempts, 1)
	require.NotEmpty(t, run.Attempts[0].Tasks)
	for i := range run.Attempts[0].Tasks {
		task := &run.Attempts[0].Tasks[i]
		require.NotEqual(t, RootSourceResolutionTaskType, task.TaskType)
		require.NotEqual(t, WithinRecipeResolutionTaskType, task.TaskType)
	}
}

func TestRecipeJobWorker_EmbeddedRecipeExplicitGitRefsUseWithinRecipeResolution(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	require.NoError(t, runGit(repoDir, "git", "init"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.name", "Root Source Test"))

	opDir := filepath.Join(repoDir, "tools", "ops", "echo")
	funcDir := filepath.Join(repoDir, "tools", "cel", "text-utils")
	require.NoError(t, os.MkdirAll(opDir, 0o755))
	require.NoError(t, os.MkdirAll(funcDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(`
name: echo
shell: bash
run: cat
input_schema:
  type: object
  required: [message]
  properties:
    message:
      type: string
output_schema:
  type: object
  properties:
    message:
      type: string
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(funcDir, "functions.yaml"), []byte(`name: text_utils
functions:
  - name: slugify
    mode: function
    execution: sh slugify.sh
    args:
      - name: input
        schema:
          type: string
    returns:
      schema:
        type: string
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(funcDir, "slugify.sh"), []byte(`#!/bin/sh
input=$(cat)
printf '%s' "$input" | tr '[:upper:]' '[:lower:]' | tr ' ' '-'
`), 0o755))
	require.NoError(t, runGit(repoDir, "git", "add", "."))
	require.NoError(t, runGit(repoDir, "git", "commit", "-m", "add explicit git selector packages"))

	output, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, string(output))
	commit := strings.TrimSpace(string(output))
	repoURL := (&url.URL{Scheme: "file", Path: repoDir}).String()

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ensureExtensionExecutionRegistered(t, registry)

	rec, err := recipe.LoadRecipeFromString([]byte(strings.TrimSpace(fmt.Sprintf(`
id: embedded_git_selector_runtime
version: "1.0"
input_schema:
  title:
    type: string
    required: true
inputs:
  title: "${{ inputs.title }}"
extensions:
  functions:
    - selector: git+%s//tools/cel/text-utils@HEAD
sequence:
  - id: echo
    op: git+%s//tools/ops/echo@HEAD
    inputs:
      message: "${{ inputs.title }}"
outputs:
  slug: "${{ slugify(sequence.echo.outputs.message) }}"
`, repoURL, repoURL))))
	require.NoError(t, err)

	workset, err := NewRecipeWorkerWithOptions(ops2.NewServiceDepsBuilder().Build(), registry, RecipeJobWorkerOptions{})
	require.NoError(t, err)

	engine := newToyEngineWithWorkSet(t, "tenant", workset, nil)
	jobCtx, _ := GenerateTestContext()
	job := workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: rec.GetMetadata().ID,
		Inputs: map[string]interface{}{
			"title": "Hello World",
		},
		JobContext: jobCtx,
	}

	jobKey, err := starter.StartRecipeJob(context.Background(), job, engine, *rec)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 30*time.Second, jobKey, engine))

	out, err := swfutil.JobResult(context.Background(), engine, jobKey)
	require.NoError(t, err)
	raw, err := out.GetData()
	require.NoError(t, err)
	got := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "hello-world", got["slug"])

	run, err := engine.GetJobRun(context.Background(), jobdb.GetJobRunRequest{
		JobKey:         jobKey,
		IncludeOutputs: true,
	})
	require.NoError(t, err)
	require.Len(t, run.Attempts, 1)
	var resolutionOutput []byte
	for i := range run.Attempts[0].Tasks {
		task := &run.Attempts[0].Tasks[i]
		require.NotEqual(t, RootSourceResolutionTaskType, task.TaskType)
		if task.TaskType == WithinRecipeResolutionTaskType {
			require.NotEmpty(t, task.Attempts)
			require.NotNil(t, task.Attempts[0].Output)
			resolutionOutput = task.Attempts[0].Output.Data
		}
	}
	require.NotEmpty(t, resolutionOutput)

	resolved, err := ParseWithinRecipeResolutionJSON(resolutionOutput)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("git+%s//tools/ops/echo@%s", repoURL, commit), resolved.ResolvedSelectors[fmt.Sprintf("git+%s//tools/ops/echo@HEAD", repoURL)])
	require.Equal(t, fmt.Sprintf("git+%s//tools/cel/text-utils@%s", repoURL, commit), resolved.ResolvedSelectors[fmt.Sprintf("git+%s//tools/cel/text-utils@HEAD", repoURL)])
}

func TestRecipeJobWorker_RemoteGitRecipeLocalRefsUseRecipeSourceCommit(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	require.NoError(t, runGit(repoDir, "git", "init"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.name", "Root Source Test"))

	opDir := filepath.Join(repoDir, "tools", "ops", "echo")
	funcDir := filepath.Join(repoDir, "tools", "cel", "text-utils")
	recipePath := filepath.Join(repoDir, "recipes", "remote.yaml")
	require.NoError(t, os.MkdirAll(opDir, 0o755))
	require.NoError(t, os.MkdirAll(funcDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(recipePath), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(`
name: echo
shell: bash
run: cat
input_schema:
  type: object
  required: [message]
  properties:
    message:
      type: string
output_schema:
  type: object
  properties:
    message:
      type: string
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(funcDir, "functions.yaml"), []byte(`name: text_utils
functions:
  - name: slugify
    mode: function
    execution: sh slugify.sh
    args:
      - name: input
        schema:
          type: string
    returns:
      schema:
        type: string
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(funcDir, "slugify.sh"), []byte(`#!/bin/sh
input=$(cat)
printf '%s' "$input" | tr '[:upper:]' '[:lower:]' | tr ' ' '-'
`), 0o755))
	require.NoError(t, os.WriteFile(recipePath, []byte(strings.TrimSpace(`
id: remote_git_recipe
version: "1.0"
input_schema:
  title:
    type: string
    required: true
inputs:
  title: "${{ inputs.title }}"
extensions:
  functions:
    - selector: ./tools/cel/text-utils
sequence:
  - id: echo
    op: ./tools/ops/echo
    inputs:
      message: "${{ inputs.title }}"
outputs:
  slug: "${{ slugify(sequence.echo.outputs.message) }}"
`)+"\n"), 0o644))
	require.NoError(t, runGit(repoDir, "git", "add", "."))
	require.NoError(t, runGit(repoDir, "git", "commit", "-m", "add remote git recipe"))

	output, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, string(output))
	commit := strings.TrimSpace(string(output))
	repoURL := (&url.URL{Scheme: "file", Path: repoDir}).String()
	selector := fmt.Sprintf("git+%s//recipes/remote.yaml@HEAD", repoURL)

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ensureExtensionExecutionRegistered(t, registry)

	workset, err := NewRecipeWorkerWithOptions(ops2.NewServiceDepsBuilder().Build(), registry, RecipeJobWorkerOptions{
		RootSourceResolver: NewRecipeSourceResolver(RecipeSourceResolverOptions{}),
	})
	require.NoError(t, err)

	engine := newToyEngineWithWorkSet(t, "tenant", workset, nil)
	jobCtx, _ := GenerateTestContext()
	job := workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: selector,
		Inputs: map[string]interface{}{
			"title": "Hello World",
		},
		JobContext: jobCtx,
	}

	jobKey, err := starter.StartRecipeJob(context.Background(), job, engine)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 30*time.Second, jobKey, engine))

	out, err := swfutil.JobResult(context.Background(), engine, jobKey)
	require.NoError(t, err)
	raw, err := out.GetData()
	require.NoError(t, err)
	got := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "hello-world", got["slug"])

	run, err := engine.GetJobRun(context.Background(), jobdb.GetJobRunRequest{
		JobKey:         jobKey,
		IncludeOutputs: true,
	})
	require.NoError(t, err)
	require.Len(t, run.Attempts, 1)
	require.NotEmpty(t, run.Attempts[0].Tasks)
	require.Equal(t, RootSourceResolutionTaskType, run.Attempts[0].Tasks[0].TaskType)
	for i := range run.Attempts[0].Tasks {
		require.NotEqual(t, WithinRecipeResolutionTaskType, run.Attempts[0].Tasks[i].TaskType)
	}

	resolvedSource, err := ParseResolvedRecipeSourceJSON(run.Attempts[0].Tasks[0].Attempts[0].Output.Data)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("git+%s//recipes/remote.yaml@%s", repoURL, commit), resolvedSource.ResolvedSelector)
	require.Contains(t, resolvedSource.RecipeYAML, "selector: ./tools/cel/text-utils")
	require.Contains(t, resolvedSource.RecipeYAML, "op: ./tools/ops/echo")
}

func TestRecipeJobWorker_RootRepoRefPinsExplicitExtensionSelectorsAfterBranchMoves(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	require.NoError(t, runGit(repoDir, "git", "init"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(repoDir, "git", "config", "user.name", "Root Source Test"))

	opDir := filepath.Join(repoDir, "tools", "ops", "echo")
	recipePath := filepath.Join(repoDir, "recipes", "remote.yaml")
	require.NoError(t, os.MkdirAll(opDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(recipePath), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(`
name: echo
shell: bash
run: cat
input_schema:
  type: object
  required: [message]
  properties:
    message:
      type: string
output_schema:
  type: object
  properties:
    message:
      type: string
`), 0o644))
	repoURL := (&url.URL{Scheme: "file", Path: repoDir}).String()
	require.NoError(t, os.WriteFile(recipePath, []byte(strings.TrimSpace(fmt.Sprintf(`
id: remote_git_recipe_with_explicit_selector
version: "1.0"
input_schema:
  title:
    type: string
    required: true
inputs:
  title: "${{ inputs.title }}"
sequence:
  - id: echo
    op: git+%s//tools/ops/echo@HEAD
    inputs:
      message: "${{ inputs.title }}"
outputs:
  echoed: "${{ sequence.echo.outputs.message }}"
`, repoURL))+"\n"), 0o644))
	require.NoError(t, runGit(repoDir, "git", "add", "."))
	require.NoError(t, runGit(repoDir, "git", "commit", "-m", "add remote recipe"))
	rootCommit := gitHead(t, repoDir)
	rootSelector := fmt.Sprintf("git+%s//recipes/remote.yaml@HEAD", repoURL)

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ensureExtensionExecutionRegistered(t, registry)

	moved := false
	workset, err := NewRecipeWorkerWithOptions(ops2.NewServiceDepsBuilder().Build(), registry, RecipeJobWorkerOptions{
		RootSourceResolver: NewRecipeSourceResolver(RecipeSourceResolverOptions{}),
		OnRecipeSourceResolved: func(RecipeSourceResolution) {
			if moved {
				return
			}
			moved = true
			require.NoError(t, os.WriteFile(filepath.Join(opDir, "moved.txt"), []byte("branch moved\n"), 0o644))
			require.NoError(t, runGit(repoDir, "git", "add", "."))
			require.NoError(t, runGit(repoDir, "git", "commit", "-m", "move branch after root resolution"))
		},
	})
	require.NoError(t, err)

	engine := newToyEngineWithWorkSet(t, "tenant", workset, nil)
	jobCtx, _ := GenerateTestContext()
	job := workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: rootSelector,
		Inputs: map[string]interface{}{
			"title": "Hello World",
		},
		JobContext: jobCtx,
	}

	jobKey, err := starter.StartRecipeJob(context.Background(), job, engine)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 30*time.Second, jobKey, engine))
	require.NotEqual(t, rootCommit, gitHead(t, repoDir))

	out, err := swfutil.JobResult(context.Background(), engine, jobKey)
	require.NoError(t, err)
	raw, err := out.GetData()
	require.NoError(t, err)
	got := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "Hello World", got["echoed"])

	run, err := engine.GetJobRun(context.Background(), jobdb.GetJobRunRequest{
		JobKey:         jobKey,
		IncludeOutputs: true,
	})
	require.NoError(t, err)
	var withinOutput []byte
	for i := range run.Attempts[0].Tasks {
		task := &run.Attempts[0].Tasks[i]
		if task.TaskType == WithinRecipeResolutionTaskType {
			require.NotEmpty(t, task.Attempts)
			require.NotNil(t, task.Attempts[0].Output)
			withinOutput = task.Attempts[0].Output.Data
		}
	}
	require.NotEmpty(t, withinOutput)

	resolved, err := ParseWithinRecipeResolutionJSON(withinOutput)
	require.NoError(t, err)
	opSelector := fmt.Sprintf("git+%s//tools/ops/echo@HEAD", repoURL)
	require.Equal(t, fmt.Sprintf("git+%s//tools/ops/echo@%s", repoURL, rootCommit), resolved.ResolvedSelectors[opSelector])
	require.Equal(t, rootCommit, resolved.ResolvedGitRefs[selectorcache.RepoRefKey(repoURL, "HEAD")])
}

func TestRecipeJobWorker_RemoteGitRecipeExplicitRemoteRefsUseWithinRecipeResolution(t *testing.T) {
	t.Parallel()

	rootRepoDir := t.TempDir()
	require.NoError(t, runGit(rootRepoDir, "git", "init"))
	require.NoError(t, runGit(rootRepoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(rootRepoDir, "git", "config", "user.name", "Root Source Test"))

	depRepoDir := t.TempDir()
	require.NoError(t, runGit(depRepoDir, "git", "init"))
	require.NoError(t, runGit(depRepoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(depRepoDir, "git", "config", "user.name", "Root Source Test"))

	depOpDir := filepath.Join(depRepoDir, "tools", "ops", "echo")
	depFuncDir := filepath.Join(depRepoDir, "tools", "cel", "text-utils")
	require.NoError(t, os.MkdirAll(depOpDir, 0o755))
	require.NoError(t, os.MkdirAll(depFuncDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(depOpDir, "op.yaml"), []byte(`
name: echo
shell: bash
run: cat
input_schema:
  type: object
  required: [message]
  properties:
    message:
      type: string
output_schema:
  type: object
  properties:
    message:
      type: string
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(depFuncDir, "functions.yaml"), []byte(`name: text_utils
functions:
  - name: slugify
    mode: function
    execution: sh slugify.sh
    args:
      - name: input
        schema:
          type: string
    returns:
      schema:
        type: string
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(depFuncDir, "slugify.sh"), []byte(`#!/bin/sh
input=$(cat)
printf '%s' "$input" | tr '[:upper:]' '[:lower:]' | tr ' ' '-'
`), 0o755))
	require.NoError(t, runGit(depRepoDir, "git", "add", "."))
	require.NoError(t, runGit(depRepoDir, "git", "commit", "-m", "add dependency selectors"))

	depOutput, err := exec.Command("git", "-C", depRepoDir, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, string(depOutput))
	depCommit := strings.TrimSpace(string(depOutput))
	depRepoURL := (&url.URL{Scheme: "file", Path: depRepoDir}).String()

	rootRecipePath := filepath.Join(rootRepoDir, "recipes", "remote.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(rootRecipePath), 0o755))
	require.NoError(t, os.WriteFile(rootRecipePath, []byte(strings.TrimSpace(fmt.Sprintf(`
id: remote_git_recipe_explicit_remote_refs
version: "1.0"
input_schema:
  title:
    type: string
    required: true
inputs:
  title: "${{ inputs.title }}"
extensions:
  functions:
    - selector: git+%s//tools/cel/text-utils@HEAD
sequence:
  - id: echo
    op: git+%s//tools/ops/echo@HEAD
    inputs:
      message: "${{ inputs.title }}"
outputs:
  slug: "${{ slugify(sequence.echo.outputs.message) }}"
`, depRepoURL, depRepoURL))+"\n"), 0o644))
	require.NoError(t, runGit(rootRepoDir, "git", "add", "."))
	require.NoError(t, runGit(rootRepoDir, "git", "commit", "-m", "add remote recipe"))

	rootOutput, err := exec.Command("git", "-C", rootRepoDir, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, string(rootOutput))
	rootCommit := strings.TrimSpace(string(rootOutput))
	rootRepoURL := (&url.URL{Scheme: "file", Path: rootRepoDir}).String()
	rootSelector := fmt.Sprintf("git+%s//recipes/remote.yaml@HEAD", rootRepoURL)

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ensureExtensionExecutionRegistered(t, registry)

	workset, err := NewRecipeWorkerWithOptions(ops2.NewServiceDepsBuilder().Build(), registry, RecipeJobWorkerOptions{
		RootSourceResolver: NewRecipeSourceResolver(RecipeSourceResolverOptions{}),
	})
	require.NoError(t, err)

	engine := newToyEngineWithWorkSet(t, "tenant", workset, nil)
	jobCtx, _ := GenerateTestContext()
	job := workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: rootSelector,
		Inputs: map[string]interface{}{
			"title": "Hello World",
		},
		JobContext: jobCtx,
	}

	jobKey, err := starter.StartRecipeJob(context.Background(), job, engine)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 30*time.Second, jobKey, engine))

	out, err := swfutil.JobResult(context.Background(), engine, jobKey)
	require.NoError(t, err)
	raw, err := out.GetData()
	require.NoError(t, err)
	got := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "hello-world", got["slug"])

	run, err := engine.GetJobRun(context.Background(), jobdb.GetJobRunRequest{
		JobKey:         jobKey,
		IncludeOutputs: true,
	})
	require.NoError(t, err)
	require.Len(t, run.Attempts, 1)
	require.NotEmpty(t, run.Attempts[0].Tasks)
	require.Equal(t, RootSourceResolutionTaskType, run.Attempts[0].Tasks[0].TaskType)

	var withinOutput []byte
	for i := range run.Attempts[0].Tasks {
		task := &run.Attempts[0].Tasks[i]
		if task.TaskType == WithinRecipeResolutionTaskType {
			require.NotEmpty(t, task.Attempts)
			require.NotNil(t, task.Attempts[0].Output)
			withinOutput = task.Attempts[0].Output.Data
		}
	}
	require.NotEmpty(t, withinOutput)

	resolvedSource, err := ParseResolvedRecipeSourceJSON(run.Attempts[0].Tasks[0].Attempts[0].Output.Data)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("git+%s//recipes/remote.yaml@%s", rootRepoURL, rootCommit), resolvedSource.ResolvedSelector)

	resolved, err := ParseWithinRecipeResolutionJSON(withinOutput)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("git+%s//tools/ops/echo@%s", depRepoURL, depCommit), resolved.ResolvedSelectors[fmt.Sprintf("git+%s//tools/ops/echo@HEAD", depRepoURL)])
	require.Equal(t, fmt.Sprintf("git+%s//tools/cel/text-utils@%s", depRepoURL, depCommit), resolved.ResolvedSelectors[fmt.Sprintf("git+%s//tools/cel/text-utils@HEAD", depRepoURL)])
}

func TestRecipeJobWorker_RemoteGitRecipeMixedRefsOnlyResolveExplicitRemoteSelectors(t *testing.T) {
	t.Parallel()

	rootRepoDir := t.TempDir()
	require.NoError(t, runGit(rootRepoDir, "git", "init"))
	require.NoError(t, runGit(rootRepoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(rootRepoDir, "git", "config", "user.name", "Root Source Test"))

	depRepoDir := t.TempDir()
	require.NoError(t, runGit(depRepoDir, "git", "init"))
	require.NoError(t, runGit(depRepoDir, "git", "config", "user.email", "root-source-test@example.com"))
	require.NoError(t, runGit(depRepoDir, "git", "config", "user.name", "Root Source Test"))

	rootOpDir := filepath.Join(rootRepoDir, "tools", "ops", "echo")
	rootFuncDir := filepath.Join(rootRepoDir, "tools", "cel", "local-utils")
	rootRecipePath := filepath.Join(rootRepoDir, "recipes", "remote.yaml")
	require.NoError(t, os.MkdirAll(rootOpDir, 0o755))
	require.NoError(t, os.MkdirAll(rootFuncDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(rootRecipePath), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rootOpDir, "op.yaml"), []byte(`
name: echo
shell: bash
run: cat
input_schema:
  type: object
  required: [message]
  properties:
    message:
      type: string
output_schema:
  type: object
  properties:
    message:
      type: string
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(rootFuncDir, "functions.yaml"), []byte(`name: local_utils
functions:
  - name: local_slugify
    mode: function
    execution: sh slugify.sh
    args:
      - name: input
        schema:
          type: string
    returns:
      schema:
        type: string
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(rootFuncDir, "slugify.sh"), []byte(`#!/bin/sh
input=$(cat)
printf 'local-%s' "$(printf '%s' "$input" | tr '[:upper:]' '[:lower:]' | tr ' ' '-')"
`), 0o755))

	depFuncDir := filepath.Join(depRepoDir, "tools", "cel", "remote-utils")
	require.NoError(t, os.MkdirAll(depFuncDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(depFuncDir, "functions.yaml"), []byte(`name: remote_utils
functions:
  - name: remote_slugify
    mode: function
    execution: sh slugify.sh
    args:
      - name: input
        schema:
          type: string
    returns:
      schema:
        type: string
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(depFuncDir, "slugify.sh"), []byte(`#!/bin/sh
input=$(cat)
printf 'remote-%s' "$(printf '%s' "$input" | tr '[:upper:]' '[:lower:]' | tr ' ' '-')"
`), 0o755))
	require.NoError(t, runGit(depRepoDir, "git", "add", "."))
	require.NoError(t, runGit(depRepoDir, "git", "commit", "-m", "add remote function"))

	depOutput, err := exec.Command("git", "-C", depRepoDir, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, string(depOutput))
	depCommit := strings.TrimSpace(string(depOutput))
	depRepoURL := (&url.URL{Scheme: "file", Path: depRepoDir}).String()

	require.NoError(t, os.WriteFile(rootRecipePath, []byte(strings.TrimSpace(fmt.Sprintf(`
id: remote_git_recipe_mixed_refs
version: "1.0"
input_schema:
  title:
    type: string
    required: true
inputs:
  title: "${{ inputs.title }}"
extensions:
  functions:
    - selector: ./tools/cel/local-utils
    - selector: git+%s//tools/cel/remote-utils@HEAD
sequence:
  - id: echo
    op: ./tools/ops/echo
    inputs:
      message: "${{ inputs.title }}"
outputs:
  local_slug: "${{ local_slugify(sequence.echo.outputs.message) }}"
  remote_slug: "${{ remote_slugify(sequence.echo.outputs.message) }}"
`, depRepoURL))+"\n"), 0o644))
	require.NoError(t, runGit(rootRepoDir, "git", "add", "."))
	require.NoError(t, runGit(rootRepoDir, "git", "commit", "-m", "add remote mixed recipe"))

	rootOutput, err := exec.Command("git", "-C", rootRepoDir, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, string(rootOutput))
	rootCommit := strings.TrimSpace(string(rootOutput))
	rootRepoURL := (&url.URL{Scheme: "file", Path: rootRepoDir}).String()
	rootSelector := fmt.Sprintf("git+%s//recipes/remote.yaml@HEAD", rootRepoURL)

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ensureExtensionExecutionRegistered(t, registry)

	workset, err := NewRecipeWorkerWithOptions(ops2.NewServiceDepsBuilder().Build(), registry, RecipeJobWorkerOptions{
		RootSourceResolver: NewRecipeSourceResolver(RecipeSourceResolverOptions{}),
	})
	require.NoError(t, err)

	engine := newToyEngineWithWorkSet(t, "tenant", workset, nil)
	jobCtx, _ := GenerateTestContext()
	job := workflowctl.StartJob{
		TenantId:   "tenant",
		RecipeName: rootSelector,
		Inputs: map[string]interface{}{
			"title": "Hello World",
		},
		JobContext: jobCtx,
	}

	jobKey, err := starter.StartRecipeJob(context.Background(), job, engine)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 30*time.Second, jobKey, engine))

	out, err := swfutil.JobResult(context.Background(), engine, jobKey)
	require.NoError(t, err)
	raw, err := out.GetData()
	require.NoError(t, err)
	got := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "local-hello-world", got["local_slug"])
	require.Equal(t, "remote-hello-world", got["remote_slug"])

	run, err := engine.GetJobRun(context.Background(), jobdb.GetJobRunRequest{
		JobKey:         jobKey,
		IncludeOutputs: true,
	})
	require.NoError(t, err)
	require.Len(t, run.Attempts, 1)
	require.NotEmpty(t, run.Attempts[0].Tasks)
	require.Equal(t, RootSourceResolutionTaskType, run.Attempts[0].Tasks[0].TaskType)

	var withinOutput []byte
	for i := range run.Attempts[0].Tasks {
		task := &run.Attempts[0].Tasks[i]
		if task.TaskType == WithinRecipeResolutionTaskType {
			require.NotEmpty(t, task.Attempts)
			require.NotNil(t, task.Attempts[0].Output)
			withinOutput = task.Attempts[0].Output.Data
		}
	}
	require.NotEmpty(t, withinOutput)

	resolvedSource, err := ParseResolvedRecipeSourceJSON(run.Attempts[0].Tasks[0].Attempts[0].Output.Data)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("git+%s//recipes/remote.yaml@%s", rootRepoURL, rootCommit), resolvedSource.ResolvedSelector)
	require.Contains(t, resolvedSource.RecipeYAML, "selector: ./tools/cel/local-utils")
	require.Contains(t, resolvedSource.RecipeYAML, "op: ./tools/ops/echo")

	resolved, err := ParseWithinRecipeResolutionJSON(withinOutput)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		fmt.Sprintf("git+%s//tools/cel/remote-utils@HEAD", depRepoURL): fmt.Sprintf("git+%s//tools/cel/remote-utils@%s", depRepoURL, depCommit),
	}, resolved.ResolvedSelectors)
}

func TestResolvedRecipeSourceLoadRecipeSupportsInternalEmptySequenceShape(t *testing.T) {
	t.Parallel()

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeSequence{
			RecipeMetadata: recipe.RecipeMetadata{
				Version: "1.0",
				NodeMetadata: recipe.NodeMetadata{
					ID: "empty_sequence_internal",
				},
			},
			SequenceData: recipe.SequenceData{
				Sequence: nil,
				Outputs: map[string]interface{}{
					"ok": true,
				},
			},
		},
	}

	raw, err := yaml.Marshal(&rec)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "sequence:")

	resolved := ResolvedRecipeSource{
		RecipeSourceResolution: RecipeSourceResolution{
			SourceKind:        RecipeSourceKindArtifact,
			SubmittedSelector: "empty-sequence",
			ResolvedSelector:  "empty-sequence",
			ArtifactName:      "empty-sequence.recipe.yaml",
		},
		RecipeYAML: string(raw),
	}

	loaded, err := resolved.LoadRecipe()
	require.NoError(t, err)
	seq, ok := loaded.RecipeImpl.(*recipe.RecipeSequence)
	require.True(t, ok)
	require.Empty(t, seq.Sequence)
	require.Equal(t, true, seq.Outputs["ok"])
}

func writeRootSourceRecipe(t *testing.T, path string, id string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(strings.TrimSpace(fmt.Sprintf(`
id: %s
version: "1.0"
sequence: []
outputs:
  ok: true
`, id))+"\n"), 0o644))
}

func gitHead(t *testing.T, repoDir string) string {
	t.Helper()
	output, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, string(output))
	return strings.TrimSpace(string(output))
}

func cachedCommitDirs(t *testing.T, cacheRoot string) []string {
	t.Helper()
	sourcesRoot := filepath.Join(cacheRoot, "sources")
	repoEntries, err := os.ReadDir(sourcesRoot)
	require.NoError(t, err)

	var commits []string
	for _, repoEntry := range repoEntries {
		if !repoEntry.IsDir() {
			continue
		}
		commitEntries, err := os.ReadDir(filepath.Join(sourcesRoot, repoEntry.Name()))
		require.NoError(t, err)
		for _, commitEntry := range commitEntries {
			if commitEntry.IsDir() && isFullGitHash(commitEntry.Name()) {
				commits = append(commits, commitEntry.Name())
			}
		}
	}
	return commits
}
