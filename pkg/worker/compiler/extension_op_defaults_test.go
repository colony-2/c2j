package compiler

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type capturingInvocationJobContext struct {
	jobKey         jobdb.JobKey
	out            jobdb.TaskData
	calls          int
	lastTaskType   string
	lastInvocation workerops.ActivityInvocationRequest
}

func (c *capturingInvocationJobContext) AwaitJobs(jobIds ...string) error {
	return nil
}

func (c *capturingInvocationJobContext) GetJobKey() jobdb.JobKey            { return c.jobKey }
func (c *capturingInvocationJobContext) Logger() *slog.Logger               { return slog.Default() }
func (c *capturingInvocationJobContext) AwaitDuration(jobdb.Duration) error { return nil }

func (c *capturingInvocationJobContext) DoTask(_ jobdb.RunPolicy, taskType string, data jobdb.TaskData) (jobdb.TaskData, error) {
	c.calls++
	c.lastTaskType = taskType

	payload, err := data.GetData()
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &c.lastInvocation); err != nil {
		return nil, err
	}
	return c.out, nil
}

func newActivityOutputTaskData(t *testing.T, gitCtx contextual.GitCommitContext) jobdb.TaskData {
	t.Helper()

	envelope := workerops.ActivityInvocationOutput{
		OpOutput: map[string]interface{}{"ok": true},
		GitResult: contextual.GitCommitContext{
			PersistHash: gitCtx.PersistHash,
			ParentHash:  gitCtx.ParentHash,
			ParentRef:   gitCtx.ParentRef,
		},
		NextTask: "",
	}
	outEnv, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, envelope)
	require.NoError(t, err)
	return jobdb.NewTaskDataOrPanic(outEnv)
}

func TestExecuteRecipeSelectorOpAppliesSchemaDefaultsBeforeResolution(t *testing.T) {
	tmpDir := t.TempDir()
	opDir := filepath.Join(tmpDir, "tools", "ops", "defaulted")
	require.NoError(t, os.MkdirAll(opDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(`
name: defaulted
run: cat
input_schema:
  type: object
  required: [message, ref]
  properties:
    message:
      type: string
    ref:
      type: string
      default: "${{ context.git.ref }}"
    config:
      type: object
      properties:
        label:
          type: string
          default: "${{ inputs.title }}"
    tags:
      type: array
      items:
        type: string
      default: ["triage"]
output_schema:
  type: object
  properties:
    ok:
      type: boolean
`), 0o644))

	withRegisteredOps(t, extops.GetExecutionOp())

	jobCtx, gitCtx := GenerateTestContext()
	jobCtx.Environment.WorktreePath = tmpDir

	stub := &capturingInvocationJobContext{
		jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "selector-defaults"},
		out:    newActivityOutputTaskData(t, gitCtx),
	}

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeOp{
			RecipeMetadata: recipe.RecipeMetadata{
				InputSchema: map[string]recipe.InputSchema{
					"title": {Type: "string", Required: true},
				},
				NodeMetadata: recipe.NodeMetadata{
					ID: "selector-defaults",
					Inputs: map[string]interface{}{
						"message": "${{ inputs.title }}",
					},
				},
			},
			OpData: recipe.OpData{Op: "./tools/ops/defaulted"},
		},
	}

	result, _, err := ExecuteRecipe(newWorkflowContext(stub), rec, map[string]interface{}{"title": "Hello"}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"ok": true}, result)
	require.Equal(t, 1, stub.calls)
	require.Equal(t, extops.ExecutionOpType+":"+extops.ExecutionOpType, stub.lastTaskType)

	require.Equal(t, "./tools/ops/defaulted", stub.lastInvocation.Input["selector"])
	rawInputs, ok := stub.lastInvocation.Input["inputs"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "Hello", rawInputs["message"])
	require.Equal(t, jobCtx.GitBase.BaseRef, rawInputs["ref"])
	require.Equal(t, []interface{}{"triage"}, rawInputs["tags"])

	config, ok := rawInputs["config"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "Hello", config["label"])
}
