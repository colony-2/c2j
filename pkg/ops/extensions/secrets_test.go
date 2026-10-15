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
	require.NoError(t, s.Prepare(context.Background(), []string{"token"}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
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
	schema["properties"].(map[string]any)["token"].(map[string]any)["default"] = 123
	_, _, err = parseSchema(schema)
	require.ErrorContains(t, err, "default must be a string")
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

func TestExtensionSecretFallbacks(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"token"}, "properties": map[string]any{
		"token": map[string]any{"type": "string", "x-secret": true, "default": "public", "minLength": 1},
	}}
	resolved, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
	require.NoError(t, err)
	for _, tc := range []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"omitted", map[string]any{}, "public"},
		{"schema", map[string]any{"token": secrets.Reference{Name: "missing"}}, "public"},
		{"explicit", map[string]any{"token": secrets.WithDefault(secrets.Reference{Name: "missing"}, "override")}, "override"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolved.ApplyInvocationDefaults(tc.input)
			require.NoError(t, err)
			require.NoError(t, resolved.ValidateInvocationInputs(tc.input))
			session := secrets.NewSession()
			defer session.Close()
			value, err := session.Materialize(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.want, value.(map[string]any)["token"])
			require.NoError(t, resolved.ValidateMaterializedInputs(value.(map[string]any)))
		})
	}
	// A default does not bypass materialized string constraints.
	require.Error(t, resolved.ValidateMaterializedInputs(map[string]any{"token": ""}))
	// Public schema defaults do not grant permission to submit arbitrary plaintext.
	require.Error(t, resolved.ValidateInvocationInputs(map[string]any{"token": "public"}))
}

func TestExtensionExecutesWithSecretFallback(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "op.yaml"), []byte(`name: fallback
input_schema:
  type: object
  required: [token]
  properties:
    token: {type: string, x-secret: true, default: public}
output_schema: {type: object}
run: |
  IFS= read -r payload
  case "$payload" in *'"token":"public"'*) ;; *) exit 9;; esac
  printf '{"output":{"ok":true}}'
`), 0600))
	session := secrets.NewSession()
	defer session.Close()
	deps := coreops.NewOpDependenciesBuilder().WithWorktreePath(root).Build()
	out, err := executeExtension(deps, secrets.WithSession(context.Background(), session), ExecutionInput{Selector: "./", Inputs: map[string]any{"token": secrets.Reference{Name: "missing"}}})
	require.NoError(t, err)
	require.Equal(t, true, out["ok"])
}

func TestExtensionNullSecretFallbackValidatesOmission(t *testing.T) {
	for _, required := range []bool{false, true} {
		schema := map[string]any{"type": "object", "properties": map[string]any{
			"token": map[string]any{"type": "string", "x-secret": true, "default": "schema"},
		}}
		if required {
			schema["required"] = []any{"token"}
		}
		resolved, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
		require.NoError(t, err)
		input := map[string]any{"token": secrets.Reference{Name: "missing", OmitIfUnavailable: true}}
		_, err = resolved.ApplyInvocationDefaults(input)
		require.NoError(t, err)
		require.NoError(t, resolved.ValidateInvocationInputs(input))
		s := secrets.NewSession()
		value, err := s.Materialize(input)
		s.Close()
		require.NoError(t, err)
		require.Empty(t, value)
		err = resolved.ValidateMaterializedInputs(value.(map[string]any))
		if required {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
}

func TestUnpreparedExtensionReferenceDefaultsFailBeforeExecution(t *testing.T) {
	for _, property := range []string{
		"{type: string, x-secret: true, default: '${{ context.secrets.?key.orValue(null) }}'}",
		"{type: string, default: '${{ context.config.?key.orValue(\"public\") }}'}",
	} {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "op.yaml"), []byte("name: unprepared\ninput_schema:\n  type: object\n  properties:\n    key: "+property+"\noutput_schema: {type: object}\nrun: touch executed\n"), 0600))
		deps := coreops.NewOpDependenciesBuilder().WithWorktreePath(root).Build()
		_, err := executeExtension(deps, context.Background(), ExecutionInput{Selector: "./"})
		require.ErrorContains(t, err, "directly with op:")
		require.NoFileExists(t, filepath.Join(root, "executed"))
	}
}
