package extensionfuncs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/require"
)

func TestExtensionFunctionsUseOnlyManifestEnv(t *testing.T) {
	t.Setenv("EXT_FUNC_AMBIENT", "ambient")

	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "tools", "functions", "envpkg")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "functions.yaml"), []byte(`
name: envpkg
shell: sh
env:
  DECLARED_ONLY: manifest
functions:
  - name: env_info
    mode: json
    execution: |
      printf '{"result":{"declared":"%s","ambient":"%s","project_root":"%s","selector":"%s","input_json":"%s"}}' \
        "${DECLARED_ONLY:-}" \
        "${EXT_FUNC_AMBIENT:-}" \
        "${VIBETHIS_PROJECT_ROOT:-}" \
        "${VIBETHIS_OP_SELECTOR:-}" \
        "${VIBETHIS_INPUT_JSON:-}"
    returns:
      schema:
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
`), 0o644))

	builder, err := BuildProvider(context.Background(), []recipe.ExtensionFunctionImport{
		{Selector: "./tools/functions/envpkg"},
	}, BuildOptions{BaseDir: tmpDir})
	require.NoError(t, err)
	require.NotNil(t, builder)

	env, err := cel.NewEnv(builder.TypeOptions()...)
	require.NoError(t, err)
	opts, err := builder.FunctionOptions(env.CELTypeAdapter())
	require.NoError(t, err)
	env, err = env.Extend(opts...)
	require.NoError(t, err)

	ast, iss := env.Compile(`env_info().declared == "manifest" && env_info().ambient == "" && env_info().project_root == "" && env_info().selector == "" && env_info().input_json == ""`)
	require.Nil(t, iss.Err())

	prg, err := env.Program(ast)
	require.NoError(t, err)
	out, _, err := prg.Eval(map[string]interface{}{})
	require.NoError(t, err)
	require.Equal(t, true, out.Value())
}

func TestExtensionFunctionsRejectManifestWorkingDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "tools", "functions", "badpkg")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "functions.yaml"), []byte(`
name: badpkg
working_directory: .
functions:
  - name: noop
    mode: json
    execution: printf '{"result":"ok"}'
    returns:
      schema:
        type: string
`), 0o644))

	_, err := BuildProvider(context.Background(), []recipe.ExtensionFunctionImport{
		{Selector: "./tools/functions/badpkg"},
	}, BuildOptions{BaseDir: tmpDir})
	require.Error(t, err)
	require.Contains(t, err.Error(), `manifest field "working_directory" is not supported`)
}

func TestExtensionFunctionsHonorManifestTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "tools", "functions", "slowpkg")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "functions.yaml"), []byte(`
name: slowpkg
shell: sh
timeout: 10ms
functions:
  - name: slow
    mode: json
    execution: exec sleep 1
    returns:
      schema:
        type: string
`), 0o644))

	pkg, err := loadPackage(context.Background(), "./tools/functions/slowpkg", BuildOptions{BaseDir: tmpDir})
	require.NoError(t, err)

	slow := pkg.functionsByName["slow"]
	require.NotNil(t, slow)

	start := time.Now()
	_, err = slow.execute([]any{})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "execute \"slow\"") || strings.Contains(err.Error(), "deadline"))
	require.Less(t, time.Since(start), 500*time.Millisecond)
}
