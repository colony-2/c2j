package compiler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestSelectorOptionalInputs(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	root := t.TempDir()
	opDir := filepath.Join(root, "compress")
	require.NoError(t, os.Mkdir(opDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(`
name: compress
run: cat
input_schema:
  type: object
  required: [file, level]
  properties:
    file: {type: string}
    level: {type: integer, default: 6}
output_schema: {type: object}
`), 0600))
	require.NoError(t, runGit(root, "git", "init", "--quiet"))
	require.NoError(t, runGit(root, "git", "add", "compress"))
	require.NoError(t, runGit(root, "git", "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--quiet", "-m", "manifest"))
	ref, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	require.NoError(t, err)

	for _, tc := range []struct {
		name, expression string
		inputs           map[string]any
		value            int64
		present          bool
		badExpression    bool
		invalidInput     bool
	}{
		{name: "absent", expression: "inputs.?level", value: 6},
		{name: "override", expression: "inputs.?level", inputs: map[string]any{"level": 9}, value: 9, present: true},
		{name: "zero", expression: "inputs.?level", inputs: map[string]any{"level": 0}, present: true},
		{name: "null", expression: "optional.of(null)", present: true, invalidInput: true},
		{name: "not optional", expression: "9", badExpression: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "id: root\nversion: '1'\ninput_schema:\n  level: {type: number}\nop: ./compress\n" +
				"inputs: {file: example.txt}\noptional_inputs: {level: '${{ " + tc.expression + " }}'}\n"
			rec, err := recipe.LoadRecipeFromString([]byte(src))
			require.NoError(t, err)
			job, git := GenerateTestContext()
			job.Environment.WorktreePath = root
			job.GitBase.BaseRepo = root
			job.GitBase.BaseRef = strings.TrimSpace(string(ref))
			job.GitBase.ResolvedBaseHash = job.GitBase.BaseRef
			git.ParentRef = job.GitBase.BaseRef
			stub := &capturingInvocationJobContext{jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, out: newActivityOutputTaskData(t, git)}
			_, _, err = ExecuteRecipe(newWorkflowContext(stub), *rec, tc.inputs, job, git)
			if tc.badExpression {
				require.ErrorContains(t, err, "expression must return a CEL optional")
				return
			}
			require.NoError(t, err)
			inv := stub.lastInvocation
			inputs := inv.Input["inputs"].(map[string]any)
			_, present := inputs["level"]
			require.Equal(t, tc.present, present)
			_, present = inv.Preparation.Extension.RawInputs["level"]
			require.Equal(t, tc.present, present)
			setup, err := workerops.ResolveInvocation(celltest.Context(context.Background()), &inv)
			require.NoError(t, err)
			inputs = inv.Input["inputs"].(map[string]any)
			require.Equal(t, "example.txt", inputs["file"])
			err = setup.Extension.ValidateInvocationInputs(inputs)
			if tc.invalidInput {
				require.ErrorContains(t, err, "integer")
				require.Contains(t, inputs, "level")
				require.Nil(t, inputs["level"])
				return
			}
			require.NoError(t, err)
			require.EqualValues(t, tc.value, inputs["level"])

			// The manifest is also applied during compile-time validation.
			rec, err = recipe.LoadRecipeFromString([]byte(src))
			require.NoError(t, err)
			_, _, err = ExecuteRecipe(newWorkflowContext(stub), *rec, tc.inputs, job, git, ExecutionOptions{Mode: ExecutionModeValidate})
			require.NoError(t, err)
		})
	}
}
