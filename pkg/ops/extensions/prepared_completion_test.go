package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/preparedtest"
	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestPreparedCompletionRetainsPreparationAndPublication(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "echo"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "echo", "op.yaml"), []byte(`
name: neutral-echo
shell: sh
run: 'exit 93'
input_schema:
  type: object
  required: [question]
  properties:
    question: {type: string}
    marker: {type: string, default: prepared-default}
    session: {type: object, x-c2j-object-type: neutral.session/v1}
output_schema:
  type: object
  required: [answer]
  properties:
    answer: {type: string}
    session: {type: object, x-c2j-object-type: neutral.session/v1}
`), 0600))
	dir := t.TempDir()
	artifacts := map[string]jobdb.Artifact{}
	store := objects.NewStore(objects.Config{JobKey: jobdb.JobKey{TenantId: "test", JobId: "test"}, TaskOrdinal: 1, Workdir: dir,
		AddArtifact: func(a jobdb.Artifact) error {
			defer a.Cleanup()
			b, err := a.Bytes(celltest.Context(context.Background()))
			if err != nil {
				return err
			}
			artifacts[a.Name()] = jobdb.NewArtifactFromBytes(a.Name(), b)
			return nil
		},
		GetArtifact: func(k jobdb.ArtifactKey) (jobdb.Artifact, error) { return artifacts[k.Name], nil },
	})
	deps := ops.NewOpDependenciesBuilder().WithWorktreePath(root).WithObjects(store).WithOperationPaths(ops.OperationPaths{Workdir: dir}).Build()
	// Task preparation applies public defaults before executeExtension. Reuse
	// that real preparation path rather than hand-populating the marker.
	invoke := func(ctx context.Context, input ExecutionInput) (map[string]any, error) {
		resolved, err := Resolve(ctx, input.Selector, ResolveOptions{BaseDir: root})
		if err != nil {
			return nil, err
		}
		if _, err := resolved.ApplyInvocationDefaults(input.Inputs); err != nil {
			return nil, err
		}
		return executeExtension(deps, WithPreparedOp(ctx, resolved), input)
	}
	entered := 0
	ctx := preparedtest.WithCompletion(celltest.Context(context.Background()), func(ctx context.Context, in preparedtest.Invocation) ([]byte, bool, error) {
		entered++
		require.Equal(t, "./echo", in.Selector)
		require.Equal(t, "prepared-default", in.Inputs["marker"])
		if session, ok := in.Inputs["session"]; ok {
			data, err := json.Marshal(session)
			require.NoError(t, err)
			require.Contains(t, string(data), "original")
		}
		outbox := in.Env[objectOutboxEnv]
		require.Equal(t, filepath.Join(dir, "objects-out"), outbox)
		file := filepath.Join(outbox, "checkpoint.txt")
		require.NoError(t, os.WriteFile(file, []byte("checkpoint"), 0600))
		raw, err := json.Marshal(map[string]any{
			"output":  map[string]any{"answer": "ok", "session": map[string]any{"$object": "next"}},
			"objects": map[string]any{"next": map[string]any{"type": "neutral.session/v1", "metadata": map[string]any{"text": "original"}, "files": map[string]string{"checkpoint": file}}},
		})
		return raw, true, err
	})
	first, err := invoke(ctx, ExecutionInput{Selector: "./echo", Inputs: map[string]any{"question": "hello"}})
	require.NoError(t, err)
	require.Equal(t, "ok", first["answer"])
	ref, ok, err := objects.Parse(first["session"])
	require.NoError(t, err)
	require.True(t, ok)
	snapshot, err := store.Open(ctx, ref, "neutral.session/v1")
	require.NoError(t, err)
	data, err := os.ReadFile(snapshot.Files["checkpoint"])
	require.NoError(t, err)
	require.Equal(t, "checkpoint", string(data))
	_, err = invoke(ctx, ExecutionInput{Selector: "./echo", Inputs: map[string]any{"question": "again", "session": first["session"]}})
	require.NoError(t, err)
	require.Equal(t, 2, entered)
	// Validation and hydration failures cannot enter the completion boundary.
	_, err = invoke(ctx, ExecutionInput{Selector: "./echo", Inputs: map[string]any{"question": 42}})
	require.ErrorContains(t, err, "input validation")
	missing := objects.ValidationRef("neutral.session/v1")
	_, err = invoke(ctx, ExecutionInput{Selector: "./echo", Inputs: map[string]any{"question": "missing", "session": missing}})
	require.Error(t, err)
	require.Equal(t, 2, entered)
	// No control leaks into an unrelated context: the actual command exits 93.
	_, err = invoke(celltest.Context(context.Background()), ExecutionInput{Selector: "./echo", Inputs: map[string]any{"question": "real"}})
	require.ErrorContains(t, err, "exit status 93")

	for _, tc := range []struct {
		name, stdout, want string
		err                error
	}{
		{name: "invalid JSON", stdout: "{", want: "invalid JSON"},
		{name: "invalid output", stdout: `{"output":{"answer":42}}`, want: "output failed schema validation"},
		{name: "unknown object", stdout: `{"output":{"answer":"ok","session":{"$object":"absent"}}}`, want: "unknown draft"},
		{name: "application error", err: errors.New("ordinary failure"), want: "ordinary failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := preparedtest.WithCompletion(celltest.Context(context.Background()), func(context.Context, preparedtest.Invocation) ([]byte, bool, error) {
				return []byte(tc.stdout), true, tc.err
			})
			_, err := invoke(ctx, ExecutionInput{Selector: "./echo", Inputs: map[string]any{"question": "hello"}})
			require.ErrorContains(t, err, tc.want)
		})
	}
}
