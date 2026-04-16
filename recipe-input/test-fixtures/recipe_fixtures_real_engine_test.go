package test_fixtures_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
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

	recipesByID := loadAllFixtureRecipes(t)

	wf := &workflow.SWFWorkflowControl{
		Registry: func(_ string, recipeRef string) (*recipe.Recipe, error) {
			if r, ok := recipesByID[recipeRef]; ok {
				return r, nil
			}
			return nil, fmt.Errorf("unknown recipe %s", recipeRef)
		},
	}

	deps := coreops.NewServiceDepsBuilder().
		WithWorkflowControl(wf).
		WithSSEManager(input.NewSimpleSSEManager()).
		Build()

	require.NoError(t, fixture.inputOp.GetManagementService().Initialize(deps))

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	workSet, err := compiler.NewRecipeWorker(deps, registry, nil)
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

			for _, tc := range testCases.Tests {
				tc := tc
				t.Run(tc.Name, func(t *testing.T) {
					if len(tc.WantArtifacts) > 0 {
						t.Skipf("real-engine fixture runner: WantArtifacts not supported yet")
					}
					if len(tc.WantJobArtifacts) > 0 {
						t.Skipf("real-engine fixture runner: WantJobArtifacts not supported yet")
					}

					jobCtx, gitCtx := generateTestContext(tc.JobContext, tc.GitContext)
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

func loadAllFixtureRecipes(t *testing.T) map[string]*recipe.Recipe {
	t.Helper()

	paths, err := filepath.Glob("recipes/*.yaml")
	require.NoError(t, err)

	out := make(map[string]*recipe.Recipe, len(paths))
	for _, p := range paths {
		if strings.HasSuffix(p, ".test.yaml") {
			continue
		}
		r, err := loadRecipeFromPath(p)
		require.NoError(t, err)
		out[r.GetMetadata().ID] = r
	}
	return out
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

func generateTestContext(jobOverride *contextual.JobContext, gitOverride *contextual.GitCommitContext) (contextual.JobContext, contextual.GitCommitContext) {
	baseJob, baseGit := compiler.GenerateTestContext()
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
