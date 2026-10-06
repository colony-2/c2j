package extensions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestExtensionObjectsRoundTripAndBranch(t *testing.T) {
	root := t.TempDir()
	opDir := filepath.Join(root, "session")
	require.NoError(t, os.Mkdir(opDir, 0700))
	for _, name := range []string{"op.yaml", "run.py"} {
		data, err := os.ReadFile(filepath.Join("testdata", "object-session", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(opDir, name), data, 0600))
	}

	artifacts := map[string]jobdb.Artifact{}
	invoke := func(ordinal int64, input map[string]any) map[string]any {
		dir := t.TempDir()
		store := objects.NewStore(objects.Config{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, TaskOrdinal: ordinal, Workdir: dir,
			AddArtifact: func(a jobdb.Artifact) error {
				defer a.Cleanup()
				b, err := a.Bytes(context.Background())
				if err != nil {
					return err
				}
				artifacts[a.Name()] = jobdb.NewArtifactFromBytes(a.Name(), b)
				return nil
			},
			GetArtifact: func(k jobdb.ArtifactKey) (jobdb.Artifact, error) { return artifacts[k.Name], nil },
		})
		deps := ops.NewOpDependenciesBuilder().WithWorktreePath(root).WithObjects(store).WithOperationPaths(ops.OperationPaths{Workdir: dir}).Build()
		out, err := executeExtension(deps, context.Background(), ExecutionInput{Selector: "./session", Inputs: input})
		require.NoError(t, err)
		require.Empty(t, deps.GetOutputArtifacts())
		return out
	}
	first := invoke(1, map[string]any{"text": "original"})
	second := invoke(2, map[string]any{"session": first["session"], "expected": "original", "text": "second"})
	third := invoke(3, map[string]any{"session": first["session"], "expected": "original", "text": "third"})
	invoke(4, map[string]any{"session": second["session"], "expected": "second", "text": "fourth"})
	require.NotEqual(t, second["session"], third["session"])
	require.Len(t, artifacts, 4)
}

func TestObjectSchemaAnnotationsValidateNestedReferences(t *testing.T) {
	schema := map[string]any{"type": "object", "$defs": map[string]any{"session": map[string]any{"type": "object", "x-c2j-object-type": "test.session/v1"}}, "properties": map[string]any{"sessions": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/session"}}}}
	schema["properties"].(map[string]any)["session"] = map[string]any{"$ref": "#/$defs/session"}
	placeholder, ok, parseErr := objects.Parse(zeroObjectFromSchema(schema)["session"])
	require.NoError(t, parseErr)
	require.True(t, ok)
	require.Equal(t, "test.session/v1", placeholder.Type)
	_, compiled, err := parseSchema(schema)
	require.NoError(t, err)
	valid := objects.ValidationRef("test.session/v1")
	require.NoError(t, compiled.Validate(map[string]any{"sessions": []any{valid}}))
	require.Error(t, compiled.Validate(map[string]any{"sessions": []any{objects.ValidationRef("other/v1")}}))
	require.Error(t, compiled.Validate(map[string]any{"sessions": []any{"string is not an object"}}))
	bad := objects.ValidationRef("test.session/v1")
	delete(bad, objects.Marker)
	require.Error(t, compiled.Validate(map[string]any{"sessions": []any{bad}}))
	_, _, err = parseSchema(map[string]any{"x-c2j-object-type": "unversioned"})
	require.Error(t, err)
}

func TestExtensionObjectPathsAndInvalidDrafts(t *testing.T) {
	host := t.TempDir()
	store := objects.NewStore(objects.Config{Workdir: host})
	deps := ops.NewOpDependenciesBuilder().WithObjects(store).WithOperationPaths(ops.OperationPaths{Workdir: host}).Build()
	env := map[string]string{objectOutboxEnv: "wrong"}
	require.NoError(t, prepareObjectOutbox(deps, env))
	require.Equal(t, filepath.Join(host, "objects-out"), env[objectOutboxEnv])
	translated, err := validateObjectPath(filepath.Join(host, "objects/checkpoint/files/home"), host)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(host, "objects/checkpoint/files/home"), translated)
	for _, path := range []string{filepath.Join(t.TempDir(), "home"), filepath.Join(host, "..", "escape"), "relative"} {
		_, err := validateObjectPath(path, host)
		require.Error(t, err)
	}
	outside := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(outside, []byte("data"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(host, "objects-out", "link")))
	cases := []struct {
		output  map[string]any
		drafts  map[string]objectDraft
		message string
	}{
		{map[string]any{"session": map[string]any{"$object": "missing"}}, nil, "unknown draft"},
		{map[string]any{}, map[string]objectDraft{"unused": {Type: "test/v1"}}, "unreferenced"},
		{map[string]any{"session": map[string]any{"$object": "next"}}, map[string]objectDraft{"next": {Type: "test/v1", Files: map[string]string{"home": filepath.Join(host, "objects-out", "link")}}}, "escapes staging"},
	}
	for _, tc := range cases {
		raw, err := json.Marshal(map[string]any{"output": tc.output, "objects": tc.drafts})
		require.NoError(t, err)
		_, err = publishExtensionObjects(context.Background(), deps, raw, tc.output)
		require.ErrorContains(t, err, tc.message)
	}
}
