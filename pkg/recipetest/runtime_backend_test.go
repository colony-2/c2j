package recipetest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	inputop "github.com/colony-2/c2j/pkg/input"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

func TestRuntimeSQLitePersistence(t *testing.T) {
	ctx := celltest.Context(context.Background())
	root := t.TempDir()
	rt, closeRuntime, err := newCaseRuntime(ctx, root, "native", &RuntimeCase{Backend: "sqlite"})
	require.NoError(t, err)
	_, err = rt.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: "native", Name: "root", Repo: "example.test/root"})
	require.NoError(t, err)
	handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "native", Cell: "root", CellRepo: "example.test/root", JobType: "persisted-probe", Data: jobdb.NewTaskDataOrPanic(map[string]any{"question": "retained"})}})
	require.NoError(t, err)
	before, err := rt.GetJob(ctx, handle.JobKey)
	require.NoError(t, err)
	require.NoError(t, closeRuntime())
	reopened, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(root, "jobs.db")})
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close(ctx)) }()
	info, err := reopened.GetJob(ctx, handle.JobKey)
	require.NoError(t, err)
	require.Equal(t, "root", info.Cell)
	require.Equal(t, before.Status, info.Status)
	require.Equal(t, before.GoalID, info.GoalID)
}

func TestRuntimeSQLiteNativeResponse(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp())
	target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `schema_version: 2
id: persistent
sequence:
- id: question
  op: input
  inputs:
    form:
      kind: review
      title: Question
      documents: {}
      fields:
      - {id: question, type: short_answer, question: "What should happen?", required: true}
outputs: {}
`}
	result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: t.TempDir()}, "native-sqlite", target, Case{ID: "sqlite-response", Type: "integration_case", Runtime: &RuntimeCase{Backend: "sqlite", Responses: []InputFixture{{NodePath: "persistent/question", Fields: map[string]any{"question": "Keep this answer"}}}}}, ExecutionOptions{})
	require.Equal(t, "passed", result.Status, "%+v", result)
	require.Len(t, result.Runtime.Reviews, 1)
}

func TestRuntimeBackendValidation(t *testing.T) {
	for _, backend := range []string{"", "memory", "sqlite"} {
		require.Empty(t, validateRuntimeCase(HarnessOptions{}, Case{Runtime: &RuntimeCase{Backend: backend}}))
	}
	require.NotEmpty(t, validateRuntimeCase(HarnessOptions{}, Case{Runtime: &RuntimeCase{Backend: "unsupported"}}))
}
