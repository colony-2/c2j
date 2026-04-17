package test_fixtures_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/recipe-core/pkg/contextual"
	coreops "github.com/colony-2/c2j/recipe-core/pkg/ops"
	"github.com/colony-2/c2j/recipe-core/pkg/recipe"
	"github.com/colony-2/c2j/recipe-core/pkg/starter"
	"github.com/colony-2/c2j/recipe-core/pkg/workflowctl"
	"github.com/colony-2/c2j/recipe-input/pkg/input"
	"github.com/colony-2/c2j/recipe-worker/pkg/compiler"
	workerops "github.com/colony-2/c2j/recipe-worker/pkg/ops"
	workflow "github.com/colony-2/c2j/recipe-worker/pkg/workflow"
	testfixtures "github.com/colony-2/c2j/recipe-worker/test-fixtures"
	"github.com/colony-2/swf-go/pkg/swf"
	directruntime "github.com/colony-2/swf-go/pkg/swf/runtime/direct"
	directtestsupport "github.com/colony-2/swf-go/pkg/swf/runtime/direct/testsupport"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRecipeFixturesRealEngine(t *testing.T) {
	fixture := ensureFixtureOps()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	wf := &workflow.SWFWorkflowControl{
		PreferRuntimeRecipeResolution: true,
	}
	rootResolver := compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{
		RecipeRefResolver: compiler.NewProviderBackedRecipeRefResolver(func(projectID string, recipeRef string) (*recipe.Recipe, error) {
			if wf.Registry == nil {
				return nil, fmt.Errorf("recipe registry not configured for %q", recipeRef)
			}
			return wf.Registry(projectID, recipeRef)
		}),
	})

	deps := coreops.NewServiceDepsBuilder().
		WithWorkflowControl(wf).
		WithSSEManager(input.NewSimpleSSEManager()).
		Build()

	require.NoError(t, fixture.inputOp.GetManagementService().Initialize(deps))

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	workSet, err := compiler.NewRecipeWorkerWithOptions(deps, registry, compiler.RecipeJobWorkerOptions{
		RootSourceResolver: rootResolver,
	})
	require.NoError(t, err)

	dsn, stopPG, err := directtestsupport.StartEmbeddedPostgres()
	require.NoError(t, err)
	t.Cleanup(stopPG)

	sqlDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, directtestsupport.InstallPGWF(ctx, sqlDB))

	strata, err := directtestsupport.StartEmbeddedStrata()
	require.NoError(t, err)
	t.Cleanup(func() { strata.Shutdown() })

	taskWorkers := make([]swf.TaskWorker, 0, len(workSet.TaskWorkers))
	for _, tw := range workSet.TaskWorkers {
		taskWorkers = append(taskWorkers, tw)
	}
	swfRuntime, err := directruntime.NewFromConfig(dsn, strata.BaseURL, strata.APIKey)
	require.NoError(t, err)

	engine, err := swf.NewEngineBuilder().
		WithRuntime(swfRuntime).
		WithAwaitRecycleThreshold(5*time.Second).
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))).
		WithMaxActive(100).
		PlusWorkers(workSet.JobWorker, taskWorkers...).
		BuildEngine()
	require.NoError(t, err)

	wf.Engine = engine
	go engine.Run(ctx)

	testFiles, err := filepath.Glob("recipes/*.test.yaml")
	require.NoError(t, err)
	if len(testFiles) == 0 {
		t.Skip("no test fixture files found")
	}

	for _, testFile := range testFiles {
		recipeName := strings.TrimSuffix(filepath.Base(testFile), ".test.yaml")
		primaryRecipePath := filepath.Join("recipes", recipeName+".yaml")

		t.Run(recipeName, func(t *testing.T) {
			primary, err := loadRecipeFromPath(primaryRecipePath)
			require.NoError(t, err)

			testData, err := os.ReadFile(testFile)
			require.NoError(t, err)

			var testCases testfixtures.TestCases
			require.NoError(t, yaml.Unmarshal(testData, &testCases))

			repoPath, repoHash := createFixtureRepo(t, primaryRecipePath, testCases.Recipes)
			registry, err := buildRecipeRegistry(primaryRecipePath, primary, testCases.Recipes, repoPath, repoHash)
			require.NoError(t, err)
			wf.Registry = registry

			for _, tc := range testCases.Tests {
				tc := tc
				t.Run(tc.Name, func(t *testing.T) {
					if len(tc.WantArtifacts) > 0 {
						t.Skipf("real-engine fixture runner: WantArtifacts not supported yet")
					}
					if len(tc.WantJobArtifacts) > 0 {
						t.Skipf("real-engine fixture runner: WantJobArtifacts not supported yet")
					}

					jobCtx, gitCtx := generateTestContext(repoPath, repoHash, tc.JobContext, tc.GitContext)
					start := workflowctl.StartJob{
						TenantId:   "test-tenant",
						RecipeName: primary.GetMetadata().ID,
						Inputs:     tc.Inputs,
						JobContext: jobCtx,
						GitRef:     gitCtx.ParentRef,
					}

					jobKey, err := starter.StartRecipeJob(ctx, start, engine, *primary)
					require.NoError(t, err)

					require.NoError(t, swf.WaitForJobToComplete(ctx, 60*time.Second, jobKey, engine))

					data, err := wf.JobResult(ctx, jobKey)
					if tc.WantErr {
						require.Error(t, err)
						if tc.WantErrContains != "" {
							assert.Contains(t, err.Error(), tc.WantErrContains)
						}
						return
					}
					require.NoError(t, err)

					got, err := readJobOutputAsMap(data)
					require.NoError(t, err)

					if tc.Want != nil {
						assertEqualWithTypeFlexibility(t, tc.Want, got, "output mismatch")
					}
				})
			}
		})
	}
}

func loadRecipeFromPath(path string) (*recipe.Recipe, error) {
	reader, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	return recipe.LoadRecipeFromReader(reader)
}

func readJobOutputAsMap(data swf.JobData) (map[string]interface{}, error) {
	rawBytes, err := data.GetData()
	if err != nil {
		return nil, err
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(rawBytes, &raw); err != nil {
		return nil, err
	}

	if wrapped, ok := raw["output"]; ok {
		if cast, ok := wrapped.(map[string]interface{}); ok {
			return cast, nil
		}
	}
	return raw, nil
}

func fixtureRecipeRelPath(recipePath string) string {
	relPath, err := filepath.Rel("recipes", recipePath)
	if err != nil || strings.HasPrefix(relPath, "..") {
		return filepath.Base(recipePath)
	}
	return filepath.ToSlash(relPath)
}

func runFixtureGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v failed: %w (%s)", args, err, out)
	}
	return nil
}

func createFixtureRepo(t *testing.T, primaryPath string, secondaryPaths []string) (string, string) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, runFixtureGit(dir, "init"))
	require.NoError(t, runFixtureGit(dir, "config", "user.email", "test@example.com"))
	require.NoError(t, runFixtureGit(dir, "config", "user.name", "Test User"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("initial\n"), 0o644))

	cells := []string{"cells/test-cell", "cells/alpha", "cells/beta", "cells/cell-a"}
	for _, rel := range cells {
		full := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(full, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(full, "README.md"), []byte(rel+"\n"), 0o644))
	}

	copyRecipe := func(sourcePath string, relPath string) {
		data, err := os.ReadFile(sourcePath)
		require.NoError(t, err)
		destPath := filepath.Join(dir, filepath.FromSlash(path.Join(compiler.CellRecipeDirectory, relPath)))
		require.NoError(t, os.MkdirAll(filepath.Dir(destPath), 0o755))
		require.NoError(t, os.WriteFile(destPath, data, 0o644))
	}

	copyRecipe(primaryPath, fixtureRecipeRelPath(primaryPath))
	for _, relPath := range secondaryPaths {
		copyRecipe(filepath.Join("recipes", relPath), filepath.ToSlash(relPath))
	}

	require.NoError(t, runFixtureGit(dir, "add", "."))
	require.NoError(t, runFixtureGit(dir, "commit", "-m", "add fixture recipes"))

	output, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, "rev-parse HEAD failed: %s", output)
	return dir, strings.TrimSpace(string(output))
}

func buildFixtureRecipeSelector(repositorySource string, recipeRelPath string, ref string) (string, error) {
	repoSource, err := compiler.NormalizeGitRepositorySource(repositorySource)
	if err != nil {
		return "", err
	}
	recipePath := path.Join(compiler.CellRecipeDirectory, filepath.ToSlash(recipeRelPath))
	return fmt.Sprintf("git+%s//%s@%s", repoSource, recipePath, ref), nil
}

func buildRecipeRegistry(primaryPath string, primary *recipe.Recipe, secondaryPaths []string, repositorySource string, ref string) (workflow.RecipeProjectProvider, error) {
	recipes := make(map[string]*recipe.Recipe)
	recipeSources := make(map[string]string)

	addLookup := func(key string, sourcePath string, rec *recipe.Recipe) error {
		if key == "" {
			return nil
		}
		if existing, ok := recipeSources[key]; ok {
			return fmt.Errorf("duplicate recipe lookup key %s in %s (already registered from %s)", key, sourcePath, existing)
		}
		recipes[key] = rec
		recipeSources[key] = sourcePath
		return nil
	}

	addRecipe := func(sourcePath string, recipeRelPath string, rec *recipe.Recipe) error {
		if rec == nil || rec.GetMetadata().ID == "" {
			return fmt.Errorf("recipe id missing in %s", sourcePath)
		}
		if err := addLookup(rec.GetMetadata().ID, sourcePath, rec); err != nil {
			return err
		}
		selector, err := buildFixtureRecipeSelector(repositorySource, recipeRelPath, ref)
		if err != nil {
			return err
		}
		return addLookup(selector, sourcePath, rec)
	}

	if err := addRecipe(primaryPath, fixtureRecipeRelPath(primaryPath), primary); err != nil {
		return nil, err
	}
	for _, relPath := range secondaryPaths {
		recipePath := filepath.Join("recipes", relPath)
		rec, err := loadRecipeFromPath(recipePath)
		if err != nil {
			return nil, err
		}
		if err := addRecipe(recipePath, filepath.ToSlash(relPath), rec); err != nil {
			return nil, err
		}
	}

	return func(_ string, recipeRef string) (*recipe.Recipe, error) {
		if r, ok := recipes[recipeRef]; ok {
			return r, nil
		}
		return nil, fmt.Errorf("unknown recipe %s", recipeRef)
	}, nil
}

func generateTestContext(baseRepo string, baseHash string, jobOverride *contextual.JobContext, gitOverride *contextual.GitCommitContext) (contextual.JobContext, contextual.GitCommitContext) {
	baseJob, baseGit := compiler.GenerateTestContext()
	baseJob.GitBase.BaseRepo = baseRepo
	baseJob.GitBase.BaseRef = baseHash
	baseJob.GitBase.ResolvedBaseHash = baseHash
	baseGit.ParentRef = baseHash
	return mergeJobContext(baseJob, jobOverride), mergeGitContext(baseGit, gitOverride)
}

func mergeJobContext(base contextual.JobContext, override *contextual.JobContext) contextual.JobContext {
	if override == nil {
		return base
	}

	if override.Actor.TicketID != "" {
		base.Actor.TicketID = override.Actor.TicketID
	}
	if override.Actor.ActorName != "" {
		base.Actor.ActorName = override.Actor.ActorName
	}
	if override.Actor.ActorEmail != "" {
		base.Actor.ActorEmail = override.Actor.ActorEmail
	}

	if !reflect.DeepEqual(override.Ticket, contextual.TicketContext{}) {
		base.Ticket = override.Ticket
	}

	if override.Environment.WorktreePath != "" {
		base.Environment.WorktreePath = override.Environment.WorktreePath
	}
	if override.Environment.WorkdirPath != "" {
		base.Environment.WorkdirPath = override.Environment.WorkdirPath
	}
	if override.Environment.ArtifactInbox != "" {
		base.Environment.ArtifactInbox = override.Environment.ArtifactInbox
	}
	if override.Environment.ArtifactOutbox != "" {
		base.Environment.ArtifactOutbox = override.Environment.ArtifactOutbox
	}

	if override.Workflow.CellName != "" {
		base.Workflow.CellName = override.Workflow.CellName
	}
	if override.Workflow.CellPath != "" {
		base.Workflow.CellPath = override.Workflow.CellPath
	}
	if override.Workflow.JobID != "" {
		base.Workflow.JobID = override.Workflow.JobID
	}
	if override.Workflow.ProjectId != "" {
		base.Workflow.ProjectId = override.Workflow.ProjectId
	}

	if override.GitBase.BaseRepo != "" {
		base.GitBase.BaseRepo = override.GitBase.BaseRepo
	}
	if override.GitBase.BaseRef != "" {
		base.GitBase.BaseRef = override.GitBase.BaseRef
	}
	if override.GitBase.ResolvedBaseHash != "" {
		base.GitBase.ResolvedBaseHash = override.GitBase.ResolvedBaseHash
	}
	if override.GitBase.GitAuthor != "" {
		base.GitBase.GitAuthor = override.GitBase.GitAuthor
	}

	return base
}

func mergeGitContext(base contextual.GitCommitContext, override *contextual.GitCommitContext) contextual.GitCommitContext {
	if override == nil {
		return base
	}
	if override.ParentRef != "" {
		base.ParentRef = override.ParentRef
	}
	if override.ParentHash != "" {
		base.ParentHash = override.ParentHash
	}
	if override.PersistHash != "" {
		base.PersistHash = override.PersistHash
	}
	return base
}

func assertEqualWithTypeFlexibility(t *testing.T, expected map[string]interface{}, actual map[string]interface{}, msg string) {
	t.Helper()
	for k, expectedValue := range expected {
		actualValue, ok := actual[k]
		if !ok {
			t.Errorf("%s: missing key %q", msg, k)
			continue
		}
		if !equalWithTypeFlexibility(expectedValue, actualValue) {
			t.Errorf("%s: key %q mismatch: expected %#v, got %#v", msg, k, expectedValue, actualValue)
		}
	}
}

func equalWithTypeFlexibility(expected, actual interface{}) bool {
	if reflect.DeepEqual(expected, actual) {
		return true
	}

	if expNum, ok := expected.(int); ok {
		switch actNum := actual.(type) {
		case float64:
			return float64(expNum) == actNum
		case int:
			return expNum == actNum
		}
	}
	if expNum, ok := expected.(float64); ok {
		switch actNum := actual.(type) {
		case int:
			return expNum == float64(actNum)
		case float64:
			return expNum == actNum
		}
	}

	expMap, expOk := expected.(map[string]interface{})
	actMap, actOk := actual.(map[string]interface{})
	if expOk && actOk {
		if len(expMap) != len(actMap) {
			return false
		}
		for k, v := range expMap {
			if !equalWithTypeFlexibility(v, actMap[k]) {
				return false
			}
		}
		return true
	}

	expSlice, expOk := expected.([]interface{})
	actSlice, actOk := actual.([]interface{})
	if expOk && actOk {
		if len(expSlice) != len(actSlice) {
			return false
		}
		for i := range expSlice {
			if !equalWithTypeFlexibility(expSlice[i], actSlice[i]) {
				return false
			}
		}
		return true
	}

	return false
}
