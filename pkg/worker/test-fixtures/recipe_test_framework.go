package testfixtures

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	gitexport "github.com/colony-2/c2j/pkg/git/export"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/worker/executor"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
	toyruntime "github.com/colony-2/swf-go/pkg/swf/runtime/toy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"gopkg.in/yaml.v3"
)

type TestCase struct {
	Name             string                       `yaml:"name"`
	Description      string                       `yaml:"description,omitempty"`
	Inputs           map[string]interface{}       `yaml:"inputs"`
	JobContext       *contextual.JobContext       `yaml:"jobContext,omitempty"`
	GitContext       *contextual.GitCommitContext `yaml:"gitContext,omitempty"`
	Want             map[string]interface{}       `yaml:"want,omitempty"`
	WantErr          bool                         `yaml:"wantErr"`
	WantErrContains  string                       `yaml:"wantErrContains,omitempty"`
	WantArtifacts    []string                     `yaml:"wantArtifacts,omitempty"`
	WantJobArtifacts []string                     `yaml:"wantJobArtifacts,omitempty"`
}

type TestCases struct {
	Recipes []string   `yaml:"recipes"`
	Tests   []TestCase `yaml:"tests"`
}

func init() {
	coreops.Register(gitexport.GetAll()...)
}

func initFixtureRepo(dir string) error {
	if err := runGit(dir, "git", "init"); err != nil {
		return err
	}
	if err := runGit(dir, "git", "config", "user.email", "test@example.com"); err != nil {
		return err
	}
	if err := runGit(dir, "git", "config", "user.name", "Test User"); err != nil {
		return err
	}
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("initial\n"), 0o644); err != nil {
		return err
	}
	cells := []string{"cells/test-cell", "cells/alpha", "cells/beta", "cells/cell-a"}
	for _, rel := range cells {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(full, 0o755); err != nil {
			return err
		}
		seed := filepath.Join(full, "README.md")
		if err := os.WriteFile(seed, []byte(rel+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func fixtureRecipeRelPath(recipePath string) string {
	relPath, err := filepath.Rel("recipes", recipePath)
	if err != nil || strings.HasPrefix(relPath, "..") {
		return filepath.Base(recipePath)
	}
	return filepath.ToSlash(relPath)
}

func copyRecipeIntoFixtureRepo(repoDir string, sourcePath string, relPath string) error {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	destPath := filepath.Join(repoDir, filepath.FromSlash(path.Join(compiler.CellRecipeDirectory, relPath)))
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destPath, data, 0o644)
}

func createFixtureRepo(primaryPath string, secondaryPaths []string) (string, string, func(), error) {
	dir, err := os.MkdirTemp("", "fixtures-repo-*")
	if err != nil {
		return "", "", nil, err
	}
	cleanup := func() {
		_ = os.RemoveAll(dir)
	}
	if err := initFixtureRepo(dir); err != nil {
		cleanup()
		return "", "", nil, err
	}
	if err := copyRecipeIntoFixtureRepo(dir, primaryPath, fixtureRecipeRelPath(primaryPath)); err != nil {
		cleanup()
		return "", "", nil, err
	}
	for _, relPath := range secondaryPaths {
		sourcePath := filepath.Join("recipes", relPath)
		if err := copyRecipeIntoFixtureRepo(dir, sourcePath, filepath.ToSlash(relPath)); err != nil {
			cleanup()
			return "", "", nil, err
		}
	}
	if err := runGit(dir, "git", "add", "."); err != nil {
		cleanup()
		return "", "", nil, err
	}
	if err := runGit(dir, "git", "commit", "-m", "add fixture recipes"); err != nil {
		cleanup()
		return "", "", nil, err
	}
	output, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("rev-parse HEAD failed: %w (%s)", err, output)
	}
	return dir, strings.TrimSpace(string(output)), cleanup, nil
}

func runGit(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v failed: %w (%s)", args, err, out)
	}
	return nil
}

func defaultTestContext(baseRepo string, baseHash string) (contextual.JobContext, contextual.GitCommitContext) {
	job := contextual.JobContext{
		Environment: contextual.EnvironmentContext{},
		Workflow: contextual.WorkflowContext{
			CellName: "cells/test-cell",
			//JobID:    "test-job-id",
		},
		GitBase: contextual.GitBaseContext{
			BaseRepo:         baseRepo,
			BaseRef:          baseHash,
			ResolvedBaseHash: baseHash,
			GitAuthor:        "",
		},
	}

	g := contextual.GitCommitContext{
		ParentRef:   baseHash,
		ParentHash:  "",
		PersistHash: "",
	}

	return job, g
}

func mergeJobContext(base contextual.JobContext, override *contextual.JobContext) contextual.JobContext {
	if override == nil {
		return base
	}

	// Ticket (replace if any field is set)
	// Environment
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

	// Workflow
	if override.Workflow.CellName != "" {
		base.Workflow.CellName = override.Workflow.CellName
	}
	if override.Workflow.JobID != "" {
		base.Workflow.JobID = override.Workflow.JobID
	}
	if override.Workflow.ProjectId != "" {
		base.Workflow.ProjectId = override.Workflow.ProjectId
	}

	// Git base
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

func generateTestContext(baseRepo string, baseHash string, jobOverride *contextual.JobContext, gitOverride *contextual.GitCommitContext) (contextual.JobContext, contextual.GitCommitContext) {
	baseJob, baseGit := defaultTestContext(baseRepo, baseHash)
	return mergeJobContext(baseJob, jobOverride), mergeGitContext(baseGit, gitOverride)
}

// equalWithTypeFlexibility compares two values with flexibility for numeric types.
// It treats int and float64 as equivalent when they represent the same numeric value,
// but only as a fallback after exact comparison fails.
func equalWithTypeFlexibility(expected, actual interface{}) bool {
	// First try exact comparison
	if reflect.DeepEqual(expected, actual) {
		return true
	}

	// If exact comparison failed, try with type flexibility

	// Handle maps recursively
	if expectedMap, ok := expected.(map[string]interface{}); ok {
		actualMap, ok := actual.(map[string]interface{})
		if !ok {
			return false
		}

		// Check if both maps have the same keys
		if len(expectedMap) != len(actualMap) {
			return false
		}

		// Compare each key-value pair with type flexibility
		for key, expectedValue := range expectedMap {
			actualValue, exists := actualMap[key]
			if !exists {
				return false
			}

			if !equalWithTypeFlexibility(expectedValue, actualValue) {
				return false
			}
		}
		return true
	}

	// Handle slices recursively
	if expectedSlice, ok := expected.([]interface{}); ok {
		actualSlice, ok := actual.([]interface{})
		if !ok {
			return false
		}

		if len(expectedSlice) != len(actualSlice) {
			return false
		}

		for i := range expectedSlice {
			if !equalWithTypeFlexibility(expectedSlice[i], actualSlice[i]) {
				return false
			}
		}
		return true
	}

	// Handle numeric comparisons with type flexibility only as fallback
	expectedNum, expectedIsNum := toFloat64(expected)
	actualNum, actualIsNum := toFloat64(actual)

	if expectedIsNum && actualIsNum {
		return expectedNum == actualNum
	}

	// Values are not equal even with type flexibility
	return false
}

// toFloat64 attempts to convert a value to float64 for numeric comparison
func toFloat64(val interface{}) (float64, bool) {
	switch v := val.(type) {
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}

func normalizeForComparison(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		cleaned := make(map[string]interface{}, len(v))
		for key, val := range v {
			if key == "context" || key == "git_persist_hash" {
				continue
			}
			cleaned[key] = normalizeForComparison(val)
		}
		return cleaned
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, item := range v {
			out[i] = normalizeForComparison(item)
		}
		return out
	default:
		return value
	}
}

func pruneActualToExpected(expected, actual interface{}) interface{} {
	expectedMap, ok := expected.(map[string]interface{})
	if !ok {
		return actual
	}
	actualMap, ok := actual.(map[string]interface{})
	if !ok {
		return actual
	}
	pruned := make(map[string]interface{}, len(actualMap))
	for key, actualValue := range actualMap {
		if expectedValue, exists := expectedMap[key]; exists {
			pruned[key] = pruneActualToExpected(expectedValue, actualValue)
			continue
		}
		if isZeroValue(actualValue) {
			continue
		}
		pruned[key] = actualValue
	}
	return pruned
}

func isZeroValue(value interface{}) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case bool:
		return v == false
	case map[string]interface{}:
		return len(v) == 0
	case []interface{}:
		return len(v) == 0
	default:
		if num, ok := toFloat64(v); ok {
			return num == 0
		}
		return false
	}
}

// assertEqualWithTypeFlexibility wraps the comparison with proper test assertion messaging
func assertEqualWithTypeFlexibility(t *testing.T, expected, actual interface{}, msgAndArgs ...interface{}) bool {
	normalizedExpected := normalizeForComparison(expected)
	normalizedActual := normalizeForComparison(actual)
	prunedActual := pruneActualToExpected(normalizedExpected, normalizedActual)
	if equalWithTypeFlexibility(normalizedExpected, prunedActual) {
		return true
	}

	return assert.Equal(t, normalizedExpected, prunedActual, msgAndArgs...)
}

func loadRecipeDefinition(path string) (recipe.Recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return recipe.Recipe{}, fmt.Errorf("failed to read recipe file %s: %w", path, err)
	}
	var def recipe.Recipe
	if err := yaml.Unmarshal(data, &def); err != nil {
		return recipe.Recipe{}, fmt.Errorf("failed to parse recipe file %s: %w", path, err)
	}
	if def.GetMetadata().ID == "" {
		return recipe.Recipe{}, fmt.Errorf("recipe id missing in %s", path)
	}
	return def, nil
}

func buildFixtureRecipeSelector(repositorySource string, recipeRelPath string, ref string) (string, error) {
	repoSource, err := compiler.NormalizeGitRepositorySource(repositorySource)
	if err != nil {
		return "", err
	}
	recipePath := path.Join(compiler.CellRecipeDirectory, filepath.ToSlash(recipeRelPath))
	return fmt.Sprintf("git+%s//%s@%s", repoSource, recipePath, ref), nil
}

func buildRecipeRegistry(primaryPath string, primary recipe.Recipe, secondaryPaths []string, repositorySource string, ref string) (workflow.RecipeProjectProvider, error) {
	recipes := make(map[string]*recipe.Recipe)
	recipeSources := make(map[string]string)

	addLookup := func(key string, sourcePath string, def recipe.Recipe) error {
		if key == "" {
			return nil
		}
		if existing, ok := recipeSources[key]; ok {
			return fmt.Errorf("duplicate recipe lookup key %s in %s (already registered from %s)", key, sourcePath, existing)
		}
		recipes[key] = &def
		recipeSources[key] = sourcePath
		return nil
	}

	addRecipe := func(sourcePath string, recipeRelPath string, def recipe.Recipe) error {
		id := def.GetMetadata().ID
		if id == "" {
			return fmt.Errorf("recipe id missing in %s", sourcePath)
		}
		if err := addLookup(id, sourcePath, def); err != nil {
			return err
		}
		if strings.TrimSpace(repositorySource) == "" || strings.TrimSpace(ref) == "" {
			return nil
		}
		selector, err := buildFixtureRecipeSelector(repositorySource, recipeRelPath, ref)
		if err != nil {
			return err
		}
		if err := addLookup(selector, sourcePath, def); err != nil {
			return err
		}
		return nil
	}

	if err := addRecipe(primaryPath, fixtureRecipeRelPath(primaryPath), primary); err != nil {
		return nil, err
	}

	for _, relPath := range secondaryPaths {
		recipePath := filepath.Join("recipes", relPath)
		def, err := loadRecipeDefinition(recipePath)
		if err != nil {
			return nil, err
		}
		if err := addRecipe(recipePath, filepath.ToSlash(relPath), def); err != nil {
			return nil, err
		}
	}

	return func(_ string, recipeRef string) (*recipe.Recipe, error) {
		if def, ok := recipes[recipeRef]; ok {
			return def, nil
		}
		return nil, fmt.Errorf("unknown recipe %s", recipeRef)
	}, nil
}

func RunTestOnAllRecipes(path string, t *testing.T) {
	RunTestOnAllRecipesWithDeps(coreops.NewServiceDepsBuilder().Build(), path, t)
}

func RunTestOnAllRecipesWithDeps(deps coreops.ServiceDependencies2, path string, t *testing.T) {

	// Create standalone executor once for all tests
	logger := zaptest.NewLogger(t)
	a, err := workerops.NewActivityRegistry()
	require.NoError(t, err)

	exec, err := executor.NewStandaloneExecutor(deps, a, logger)
	require.NoError(t, err, "Failed to create standalone executor")

	// Find all .test.yaml files
	testFiles, err := filepath.Glob(path)
	require.NoError(t, err, "Failed to find test files")

	if len(testFiles) == 0 {
		t.Skip("No test files found in recipes/")
	}

	for _, testFile := range testFiles {
		// Extract recipe name from test file
		recipeName := strings.TrimSuffix(filepath.Base(testFile), ".test.yaml")
		recipePath := filepath.Join("recipes", recipeName+".yaml")

		t.Run(recipeName, func(t *testing.T) {
			// Load test cases
			testData, err := os.ReadFile(testFile)
			require.NoError(t, err, "Failed to read test file: %s", testFile)

			var testCases TestCases
			err = yaml.Unmarshal(testData, &testCases)
			require.NoError(t, err, "Failed to parse test file: %s", testFile)

			// Load recipe
			reader, err := os.Open(recipePath)
			require.NoError(t, err, "Failed to read recipe file: %s", recipePath)
			defer reader.Close()
			recipeDefP, err := recipe.LoadRecipeFromReader(reader)
			require.NoError(t, err, "Failed to load recipe file: %s", recipePath)
			recipeDef := *recipeDefP

			repoPath, repoHash, cleanup, err := createFixtureRepo(recipePath, testCases.Recipes)
			require.NoError(t, err, "Failed to create fixture repo")
			t.Cleanup(cleanup)

			registry, err := buildRecipeRegistry(recipePath, recipeDef, testCases.Recipes, repoPath, repoHash)
			require.NoError(t, err, "Failed to build recipe registry")

			// Run table-driven tests
			for _, tc := range testCases.Tests {
				tc := tc // capture range variable
				t.Run(tc.Name, func(t *testing.T) {
					// Execute recipe using standalone executor
					jobCtx, gitCtx := generateTestContext(repoPath, repoHash, tc.JobContext, tc.GitContext)
					var result map[string]interface{}
					var artifacts []string
					var jobArtifacts []string
					if len(tc.WantArtifacts) > 0 || len(tc.WantJobArtifacts) > 0 {
						result, artifacts, jobArtifacts, err = executeRecipeWithArtifacts(context.Background(), a, recipeDef, tc.Inputs, jobCtx, gitCtx.ParentRef, registry, deps)
					} else {
						result, err = exec.ExecuteWithRegistry(context.Background(), recipeDef, tc.Inputs, jobCtx, gitCtx.ParentRef, registry)
					}

					// Check results
					if tc.WantErr {
						require.Error(t, err, "Expected error but got none")
						if tc.WantErrContains != "" {
							assert.Contains(t, err.Error(), tc.WantErrContains,
								"Error message doesn't contain expected text")
						}
					} else {
						require.NoError(t, err, "Unexpected error executing recipe")

						if tc.Want != nil {
							assertEqualWithTypeFlexibility(t, tc.Want, result, "Output mismatch")
						}
						if len(tc.WantArtifacts) > 0 {
							assertArtifactNames(t, tc.WantArtifacts, artifacts)
						}
						if len(tc.WantJobArtifacts) > 0 {
							assertArtifactNames(t, tc.WantJobArtifacts, jobArtifacts)
						}
					}
				})
			}
		})
	}
}

type artifactCapture struct {
	mu    sync.Mutex
	names map[string]bool
}

func newArtifactCapture() *artifactCapture {
	return &artifactCapture{names: make(map[string]bool)}
}

func (c *artifactCapture) add(artifacts []swf.Artifact) {
	if len(artifacts) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, artifact := range artifacts {
		c.names[artifact.Name()] = true
	}
}

func (c *artifactCapture) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.names))
	for name := range c.names {
		out = append(out, name)
	}
	return out
}

type capturingTaskWorker struct {
	inner   swf.TaskWorker
	capture *artifactCapture
}

func (c *capturingTaskWorker) Name() string {
	return c.inner.Name()
}

func (c *capturingTaskWorker) Run(ctx swf.TaskContext, input swf.TaskData) (swf.TaskData, error) {
	output, err := c.inner.Run(ctx, input)
	if output != nil {
		if artifacts, artErr := output.GetArtifacts(); artErr == nil {
			c.capture.add(artifacts)
		}
	}
	return output, err
}

func wrapTaskWorkers(workers map[string]swf.TaskWorker, capture *artifactCapture) map[string]swf.TaskWorker {
	wrapped := make(map[string]swf.TaskWorker, len(workers))
	for name, worker := range workers {
		wrapped[name] = &capturingTaskWorker{inner: worker, capture: capture}
	}
	return wrapped
}

func assertArtifactNames(t *testing.T, expected []string, actual []string) {
	t.Helper()
	seen := make(map[string]bool, len(actual))
	for _, name := range actual {
		seen[name] = true
	}
	for _, name := range expected {
		assert.True(t, seen[name], "missing expected artifact: %s", name)
	}
}

func executeRecipeWithArtifacts(
	ctx context.Context,
	registry *workerops.ActivityRegistry,
	recipeDef recipe.Recipe,
	inputs map[string]interface{},
	jobCtx contextual.JobContext,
	gitRef string,
	recipeRegistry workflow.RecipeProjectProvider,
	deps coreops.ServiceDependencies2,
) (map[string]interface{}, []string, []string, error) {
	rootResolver := compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{
		RecipeRefResolver: compiler.NewProviderBackedRecipeRefResolver(func(projectID string, recipeRef string) (*recipe.Recipe, error) {
			return recipeRegistry(projectID, recipeRef)
		}),
	})

	control := &workflow.SWFWorkflowControl{
		Registry:                      recipeRegistry,
		PreferRuntimeRecipeResolution: true,
	}

	deps = coreops.NewServiceDepsBuilder().WithWorkflowControl(control).WithDatabase(deps.Database()).WithSSEManager(deps.SSEManager()).Build()
	workset, err := compiler.NewRecipeWorkerWithOptions(deps, registry, compiler.RecipeJobWorkerOptions{
		RootSourceResolver: rootResolver,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	capture := newArtifactCapture()
	workset.TaskWorkers = wrapTaskWorkers(workset.TaskWorkers, capture)

	taskWorkers := make([]swf.TaskWorker, 0, len(workset.TaskWorkers))
	for _, tw := range workset.TaskWorkers {
		taskWorkers = append(taskWorkers, tw)
	}
	tenantID := "default"
	engine, err := swf.NewEngineBuilder().
		WithRuntime(toyruntime.New()).
		WithWorkerTenantId(tenantID).
		PlusWorkers(workset.JobWorker, taskWorkers...).
		BuildEngine()
	if err != nil {
		return nil, nil, nil, err
	}
	go engine.Run(ctx)
	control.Engine = engine
	job := workflowctl.StartJob{
		TenantId:   tenantID,
		RecipeName: recipeDef.GetMetadata().ID,
		Inputs:     inputs,
		JobContext: jobCtx,
		GitRef:     gitRef,
	}

	jobKey, err := control.StartJob(ctx, job)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := swf.WaitForJobToComplete(ctx, 30*time.Second, jobKey, engine); err != nil {
		return nil, nil, nil, err
	}
	out, err := swfutil.JobResult(ctx, engine, jobKey)
	if err != nil {
		return nil, nil, nil, err
	}
	jobArtifacts, err := out.GetArtifacts()
	if err != nil {
		return nil, nil, nil, err
	}
	jobArtifactNames := make([]string, 0, len(jobArtifacts))
	for _, artifact := range jobArtifacts {
		jobArtifactNames = append(jobArtifactNames, artifact.Name())
	}
	data, err := out.GetData()
	if err != nil {
		return nil, nil, nil, err
	}
	outMap := make(map[string]interface{})
	if err := yaml.Unmarshal(data, &outMap); err != nil {
		return nil, nil, nil, err
	}
	return outMap, capture.list(), jobArtifactNames, nil
}
