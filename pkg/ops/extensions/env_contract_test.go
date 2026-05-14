package extensions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestExecutionOpInheritsParentEnvAndManifestOverrides(t *testing.T) {
	t.Setenv("EXT_OP_AMBIENT", "ambient")
	t.Setenv("DECLARED_ONLY", "ambient-declared")
	t.Setenv("GOPATH", "fixture-gopath")
	t.Setenv("GOMODCACHE", "fixture-gomodcache")
	t.Setenv("VIBETHIS_PROJECT_ROOT", "")
	t.Setenv("VIBETHIS_OP_SELECTOR", "")
	t.Setenv("VIBETHIS_INPUT_JSON", "")

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
	require.Equal(t, "ambient", output["ambient"])
	require.Equal(t, "fixture-gopath", output["gopath"])
	require.Equal(t, "fixture-gomodcache", output["gomodcache"])
	require.Equal(t, "", output["project_root"])
	require.Equal(t, "", output["selector"])
	require.Equal(t, "", output["input_json"])
}

func TestBuildProcessEnvMapMergesParentAndOverrides(t *testing.T) {
	t.Setenv("EXT_OP_PARENT_ONLY", "parent")
	t.Setenv("EXT_OP_OVERRIDE", "parent")

	merged := buildProcessEnvMap(map[string]string{
		"EXT_OP_OVERRIDE":      "manifest",
		"EXT_OP_MANIFEST_ONLY": "manifest-only",
	})

	require.Equal(t, "parent", merged["EXT_OP_PARENT_ONLY"])
	require.Equal(t, "manifest", merged["EXT_OP_OVERRIDE"])
	require.Equal(t, "manifest-only", merged["EXT_OP_MANIFEST_ONLY"])

	processEnv := buildProcessEnv(map[string]string{"EXT_OP_OVERRIDE": "manifest"})
	require.Contains(t, processEnv, "EXT_OP_PARENT_ONLY=parent")
	require.Contains(t, processEnv, "EXT_OP_OVERRIDE=manifest")
	for _, item := range processEnv {
		require.False(t, strings.HasPrefix(item, "EXT_OP_OVERRIDE=parent"))
	}
}

func selectorEnvContractOpYAML(name string) string {
	return `name: ` + name + `
shell: sh
env:
  DECLARED_ONLY: manifest
run: |
  printf '{"declared":"%s","ambient":"%s","gopath":"%s","gomodcache":"%s","project_root":"%s","selector":"%s","input_json":"%s"}' \
    "${DECLARED_ONLY:-}" \
    "${EXT_OP_AMBIENT:-}" \
    "${GOPATH:-}" \
    "${GOMODCACHE:-}" \
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
    gopath:
      type: string
    gomodcache:
      type: string
    project_root:
      type: string
    selector:
      type: string
    input_json:
      type: string
`
}
