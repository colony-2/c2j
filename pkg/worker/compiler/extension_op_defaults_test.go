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
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/stretchr/testify/require"
)

type capturingInvocationJobContext struct {
	jobKey         swf.JobKey
	out            swf.TaskData
	calls          int
	lastTaskType   string
	lastInvocation workerops.ActivityInvocationRequest
}

func (c *capturingInvocationJobContext) AwaitJobs(jobIds ...string) error {
	return nil
}

func (c *capturingInvocationJobContext) GetJobKey() swf.JobKey            { return c.jobKey }
func (c *capturingInvocationJobContext) Logger() *slog.Logger             { return slog.Default() }
func (c *capturingInvocationJobContext) AwaitDuration(swf.Duration) error { return nil }

func (c *capturingInvocationJobContext) DoTask(_ swf.RunPolicy, taskType string, data swf.TaskData) (swf.TaskData, error) {
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

func newActivityOutputTaskData(t *testing.T, gitCtx contextual.GitCommitContext) swf.TaskData {
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
	return swf.NewTaskDataOrPanic(outEnv)
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
`), 0o644))

	withRegisteredOps(t, extops.GetExecutionOp())

	jobCtx, gitCtx := GenerateTestContext()
	jobCtx.Environment.WorktreePath = tmpDir

	stub := &capturingInvocationJobContext{
		jobKey: swf.JobKey{TenantId: "tenant", JobId: "selector-defaults"},
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

func TestExecuteRecipeDiscoveredExtensionOpAppliesSchemaDefaultsBeforeResolution(t *testing.T) {
	tmpDir := t.TempDir()
	opDir := filepath.Join(tmpDir, ".colony2", "ops", "defaulted")
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
`), 0o644))

	discovered, err := extops.Discover(tmpDir)
	require.NoError(t, err)
	require.Len(t, discovered, 1)
	withRegisteredOps(t, discovered[0])

	jobCtx, gitCtx := GenerateTestContext()

	stub := &capturingInvocationJobContext{
		jobKey: swf.JobKey{TenantId: "tenant", JobId: "discovered-defaults"},
		out:    newActivityOutputTaskData(t, gitCtx),
	}

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeOp{
			RecipeMetadata: recipe.RecipeMetadata{
				InputSchema: map[string]recipe.InputSchema{
					"title": {Type: "string", Required: true},
				},
				NodeMetadata: recipe.NodeMetadata{
					ID: "discovered-defaults",
					Inputs: map[string]interface{}{
						"message": "${{ inputs.title }}",
					},
				},
			},
			OpData: recipe.OpData{Op: "defaulted"},
		},
	}

	result, _, err := ExecuteRecipe(newWorkflowContext(stub), rec, map[string]interface{}{"title": "Hello"}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"ok": true}, result)
	require.Equal(t, 1, stub.calls)
	require.Equal(t, "defaulted:defaulted", stub.lastTaskType)

	require.Equal(t, "Hello", stub.lastInvocation.Input["message"])
	require.Equal(t, jobCtx.GitBase.BaseRef, stub.lastInvocation.Input["ref"])
	require.Equal(t, []interface{}{"triage"}, stub.lastInvocation.Input["tags"])

	config, ok := stub.lastInvocation.Input["config"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "Hello", config["label"])
}

func TestExecuteRecipeDiscoveredExtensionOpSkipsRequiredDefaultAtParseTime(t *testing.T) {
	tmpDir := t.TempDir()
	opDir := filepath.Join(tmpDir, ".colony2", "ops", "defaulted")
	require.NoError(t, os.MkdirAll(opDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(`
name: defaulted
run: cat
input_schema:
  type: object
  required: [ref]
  properties:
    ref:
      type: string
      default: "${{ context.git.ref }}"
`), 0o644))

	discovered, err := extops.Discover(tmpDir)
	require.NoError(t, err)
	require.Len(t, discovered, 1)
	withRegisteredOps(t, discovered[0])

	_, err = recipe.LoadRecipeFromString([]byte(`
id: parse-time-defaults
version: "1.0"
input_schema:
  title:
    type: string
    required: true
inputs:
  title: "${{ inputs.title }}"
sequence:
  - id: call
    op: defaulted
outputs:
  ok: true
`))
	require.NoError(t, err)
}
