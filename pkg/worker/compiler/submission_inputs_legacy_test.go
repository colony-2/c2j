package compiler

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type submissionInputsFixture struct {
	Key    jobdb.JobKey
	Output map[string]any
}

type submissionInputsSentinelTask struct{ calls atomic.Int32 }

func (*submissionInputsSentinelTask) Name() string { return "submission_inputs_sentinel:run" }
func (s *submissionInputsSentinelTask) Run(_ jobworkflow.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	s.calls.Add(1)
	return scopeProbeOutput(map[string]any{"called": true}, contextual.GitCommitContext{})
}

func submissionInputsSentinel(t *testing.T) *submissionInputsSentinelTask {
	t.Helper()
	op, err := coreops.NewOp().WithType("submission_inputs_sentinel").AddStep("run", coreops.NewNoTaskStep[struct{}, map[string]any]()).Build()
	require.NoError(t, err)
	withRegisteredOps(t, op.(coreops.RegisterableOp))
	return &submissionInputsSentinelTask{}
}

func submitSubmissionInputs(t *testing.T, rt jobdb.ConsumerRuntime, declared bool) jobdb.JobKey {
	t.Helper()
	schema := "input_schema: {}\n"
	if declared {
		schema = "input_schema:\n  title: {type: string}\n  prompt: {type: string}\n"
	}
	rec, err := recipe.LoadRecipeFromString([]byte("id: submission-inputs\n" + schema + `inputs: {all: '${{ inputs }}'}
sequence:
 - id: sentinel
   op: submission_inputs_sentinel
outputs:
 all: '${{ inputs.all }}'
 called: '${{ sequence.sentinel.outputs.called }}'
`))
	require.NoError(t, err)
	ctx := celltest.Context(context.Background())
	sdk, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt.(jobdb.JobSchemaRegistry)}
	celltest.Create(t, engine, "submission-inputs", "test", celltest.Repo(""))
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "submission-inputs", Cell: "test", CellRepo: celltest.Repo(""), RecipeName: "submission-inputs", Inputs: map[string]any{"title": "  Café\nTitle  ", "prompt": "  Inspect\n日本語  "}}, engine, *rec)
	require.NoError(t, err)
	return ref.Key()
}

// Opt-in only: run with the unchanged evaluator (see testdata README).
func TestGenerateRecipeSubmissionInputsLegacyFixtures(t *testing.T) {
	dir := os.Getenv("C2J_GENERATE_SUBMISSION_INPUT_FIXTURES")
	if dir == "" {
		t.Skip("explicit baseline fixture generation only")
	}
	require.NoError(t, os.MkdirAll(dir, 0755))
	for _, declared := range []bool{true, false} {
		name := "undeclared-failure"
		if declared {
			name = "declared-success"
		}
		t.Run(name, func(t *testing.T) {
			sentinel := submissionInputsSentinel(t)
			path := filepath.Join(t.TempDir(), "jobs.db")
			rt, err := sqlite.NewFromConfig(testBackground(), sqlite.Config{DBPath: path, MaxInlineArtifactBytes: 10 << 20, Projects: jobdbcelltest.UnrestrictedProjects{}})
			require.NoError(t, err)
			key := submitSubmissionInputs(t, rt, declared)
			out := probeRun(t, rt, key, NewRecipeJobWorker(RecipeJobWorkerOptions{}), sentinel)
			fixture := submissionInputsFixture{Key: key}
			if declared {
				fixture.Output = gitLifecycleOutput(t, out)
				require.EqualValues(t, 1, sentinel.calls.Load())
			} else {
				require.Equal(t, jobworkflow.JobRunFailed, out.Status)
				require.ErrorContains(t, out.JobError, "not defined in schema")
				require.Zero(t, sentinel.calls.Load(), "must use the baseline evaluator")
			}
			raw, err := json.MarshalIndent(fixture, "", "  ")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, name+".json"), raw, 0644))
			require.NoError(t, rt.Close(testBackground()))
			src, err := os.Open(path)
			require.NoError(t, err)
			defer src.Close()
			dst, err := os.Create(filepath.Join(dir, name+".db.gz"))
			require.NoError(t, err)
			zipped := gzip.NewWriter(dst)
			_, err = io.Copy(zipped, src)
			require.NoError(t, err)
			require.NoError(t, zipped.Close())
			require.NoError(t, dst.Close())
		})
	}
}
