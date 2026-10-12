package extensions

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestExtensionSecretStdinAndPlaintextRejection(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "op")
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "op.yaml"), []byte(`name: secret-probe
input_schema:
  type: object
  required: [token]
  properties:
    token: {type: string, x-secret: true}
output_schema: {type: object}
run: |
  IFS= read -r payload
  case "$payload" in *'$c2j_secret'*) exit 9;; esac
  printf '{"output":{"ok":true}}'
`), 0600))
	s := secrets.NewSession()
	defer s.Close()
	require.NoError(t, s.Prepare(context.Background(), []string{"token"}, nil, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "private-token\n"}}}, nil
	}))
	ctx := secrets.WithSession(context.Background(), s)
	deps := coreops.NewOpDependenciesBuilder().WithWorktreePath(root).Build()
	out, err := executeExtension(deps, ctx, ExecutionInput{Selector: "./op", Inputs: map[string]any{"token": secrets.Reference{Name: "token"}}})
	require.NoError(t, err)
	require.Equal(t, true, out["ok"])
	_, err = executeExtension(deps, ctx, ExecutionInput{Selector: "./op", Inputs: map[string]any{"token": "plaintext"}})
	require.ErrorContains(t, err, "requires a reference")
}

func TestExtensionSecretSchemaDeferredAndMaterialized(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"token"}, "properties": map[string]any{"token": map[string]any{"type": "string", "x-secret": true, "minLength": 3}, "path": map[string]any{"type": "string"}}}
	_, compiled, err := parseSchema(schema)
	require.NoError(t, err)
	r := &ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}, compiledInput: compiled}
	require.NoError(t, r.ValidateInvocationInputs(map[string]any{"token": secrets.Reference{Name: "token"}}))
	require.Error(t, r.ValidateInvocationInputs(map[string]any{"token": "plaintext"}))
	require.Error(t, r.ValidateInvocationInputs(map[string]any{"token": secrets.Reference{Name: "token"}, "path": secrets.Reference{Name: "token"}}))
	require.NoError(t, r.ValidateMaterializedInputs(map[string]any{"token": "real-token"}))
	require.Error(t, r.ValidateMaterializedInputs(map[string]any{"token": "x"}))
	schema["properties"].(map[string]any)["token"].(map[string]any)["default"] = "plaintext"
	_, _, err = parseSchema(schema)
	require.ErrorContains(t, err, "no default")
}

func TestUnsupportedSecretAnnotationsFailClosed(t *testing.T) {
	for _, schema := range []map[string]any{
		{"type": "string", "x-secret": "true"},
		{"$defs": map[string]any{"token": map[string]any{"type": "string", "x-secret": true}}, "$ref": "#/$defs/token"},
		{"allOf": []any{map[string]any{"type": "string", "x-secret": true}}},
	} {
		_, _, err := parseSchema(schema)
		require.Error(t, err)
	}
	_, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{OutputSchema: map[string]any{"type": "string", "x-secret": true}}})
	require.ErrorContains(t, err, "secret output annotations")
}
