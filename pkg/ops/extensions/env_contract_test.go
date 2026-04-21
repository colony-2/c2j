package extensions

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestExecutionOpUsesOnlyManifestEnv(t *testing.T) {
	t.Setenv("EXT_OP_AMBIENT", "ambient")

	tmpDir := t.TempDir()
	opDir := filepath.Join(tmpDir, "testdata", "env-op")
	require.NoError(t, os.MkdirAll(opDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(selectorEnvContractOpYAML("selector-env-check")), 0o644))

	deps := coreops.NewOpDependenciesBuilder().
		WithWorktreePath(tmpDir).
		Build()

	output, err := GetExecutionOp().TaskChain()[0].Invoke(deps, context.Background(), map[string]interface{}{
		"selector": "./testdata/env-op",
		"inputs":   map[string]interface{}{},
	})
	require.NoError(t, err)
	require.Equal(t, "manifest", output["declared"])
	require.Equal(t, "", output["ambient"])
	require.Equal(t, "", output["project_root"])
	require.Equal(t, "", output["selector"])
	require.Equal(t, "", output["input_json"])
}

func selectorEnvContractOpYAML(name string) string {
	return `name: ` + name + `
shell: sh
env:
  DECLARED_ONLY: manifest
run: |
  printf '{"declared":"%s","ambient":"%s","project_root":"%s","selector":"%s","input_json":"%s"}' \
    "${DECLARED_ONLY:-}" \
    "${EXT_OP_AMBIENT:-}" \
    "${VIBETHIS_PROJECT_ROOT:-}" \
    "${VIBETHIS_OP_SELECTOR:-}" \
    "${VIBETHIS_INPUT_JSON:-}"
input_schema:
  type: object
  properties: {}
output_schema:
  type: object
  properties:
    declared:
      type: string
    ambient:
      type: string
    project_root:
      type: string
    selector:
      type: string
    input_json:
      type: string
`
}
