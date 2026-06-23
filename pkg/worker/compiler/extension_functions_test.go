package compiler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestExecuteRecipeExtensionFunctions_LocalSelectorModes(t *testing.T) {
	worktree := t.TempDir()
	writeFunctionPackage(t, filepath.Join(worktree, "tools", "cel", "text-utils"), `name: text_utils
description: Test helpers
version: 1.0.0

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
  - name: semver_compare
    mode: json
    execution: sh semver_compare.sh
    args:
      - name: left
        schema:
          type: string
      - name: right
        schema:
          type: string
    returns:
      schema:
        type: integer
`, map[string]string{
		"slugify.sh": `#!/bin/sh
input=$(cat)
printf '%s' "$input" | tr '[:upper:]' '[:lower:]' | tr ' ' '-'
`,
		"semver_compare.sh": `#!/bin/sh
input=$(cat)
if [ "$input" = '{"args":["1.2.3","2.0.0"]}' ]; then
  printf '{"result":-1}'
elif [ "$input" = '{"args":["2.0.0","1.2.3"]}' ]; then
  printf '{"result":1}'
else
  printf '{"result":0}'
fi
`,
	})

	rec := mustLoadRecipe(t, `
version: "1"
input_schema:
  title:
    type: string
    required: true
  left:
    type: string
    required: true
  right:
    type: string
    required: true
inputs:
  title: "${{ inputs.title }}"
  left: "${{ inputs.left }}"
  right: "${{ inputs.right }}"
extensions:
  functions:
    - selector: ./tools/cel/text-utils
      include: [slugify, semver_compare]
      rename:
        slugify: text_slugify
sequence: []
outputs:
  slug: "${{ text_slugify(inputs.title) }}"
  cmp: "${{ semver_compare(inputs.left, inputs.right) }}"
`)

	jobCtx, gitCtx := GenerateTestContext()
	jobCtx.Environment.WorktreePath = worktree

	ctx := workflow.Context{
		JobContext:           &countingJobContext{jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}},
		ServiceDependencies2: newWorkflowContext(&countingJobContext{}).ServiceDependencies2,
	}

	result, _, err := ExecuteRecipe(ctx, rec, map[string]interface{}{
		"title": "Hello World",
		"left":  "1.2.3",
		"right": "2.0.0",
	}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{
		"slug": "hello-world",
		"cmp":  int64(-1),
	}, result)
}

func TestExecuteRecipeExtensionFunctions_ValidationDoesNotExecute(t *testing.T) {
	worktree := t.TempDir()
	marker := filepath.Join(worktree, "executed.txt")
	writeFunctionPackage(t, filepath.Join(worktree, "tools", "cel", "side-effects"), `name: side_effects
version: 1.0.0

functions:
  - name: side_effect
    mode: function
    execution: sh side_effect.sh
    args:
      - name: input
        schema:
          type: string
    returns:
      schema:
        type: string
`, map[string]string{
		"side_effect.sh": "#!/bin/sh\n" +
			"touch " + shellQuote(marker) + "\n" +
			"cat\n",
	})

	rec := mustLoadRecipe(t, `
version: "1"
input_schema:
  title:
    type: string
    required: true
inputs:
  title: "${{ inputs.title }}"
extensions:
  functions:
    - selector: ./tools/cel/side-effects
sequence: []
outputs:
  value: "${{ side_effect(inputs.title) }}"
`)

	jobCtx, gitCtx := GenerateTestContext()
	jobCtx.Environment.WorktreePath = worktree

	ctx := newWorkflowContext(&countingJobContext{jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}})
	result, _, err := ExecuteRecipe(ctx, rec, map[string]interface{}{
		"title": "Hello World",
	}, jobCtx, gitCtx, ExecutionOptions{Mode: ExecutionModeValidate})
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"value": ""}, result)
	_, statErr := os.Stat(marker)
	require.True(t, os.IsNotExist(statErr), "function script should not execute in validate mode")
}

func writeFunctionPackage(t *testing.T, dir string, manifest string, files map[string]string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "functions.yaml"), []byte(manifest), 0o644))
	for name, content := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	}
}

func mustLoadRecipe(t *testing.T, raw string) recipe.Recipe {
	t.Helper()
	rec, err := recipe.LoadRecipeFromString([]byte(raw))
	require.NoError(t, err)
	return *rec
}

func shellQuote(path string) string {
	return "'" + filepath.ToSlash(path) + "'"
}
