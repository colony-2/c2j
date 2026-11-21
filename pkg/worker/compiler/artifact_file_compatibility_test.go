package compiler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

func TestArtifactFileSecretsCheckpointDefaultsAndBindings(t *testing.T) {
	emit := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "file_checkpoint"}, func(deps coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
		root, err := os.MkdirTemp("", "file-checkpoint-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(root)
		path := filepath.Join(root, "state")
		if err := os.WriteFile(path, []byte("checkpoint bytes"), 0600); err != nil {
			return nil, err
		}
		ref, err := deps.Objects().Publish(ctx, "test.session/v1", map[string]any{"id": "test"}, map[string]string{"state": path})
		return map[string]any{"checkpoint": ref}, err
	})
	withRegisteredOps(t, extops.GetExecutionOp(), emit)
	repo := t.TempDir()
	workspaceRepo(t, repo, "source")
	trace := filepath.Join(t.TempDir(), "trace")
	file := filepath.Join(t.TempDir(), "default")
	body := []byte("default document\r\n")
	require.NoError(t, os.WriteFile(file, body, 0600))
	ref := artifacts.NewExternalRef("default", "file://"+file, false)
	schema := map[string]any{"type": "object", "required": []string{"instructions_file", "token", "checkpoint"}, "properties": map[string]any{
		"instructions_file":  map[string]any{"type": "object", "x-c2j-artifact-file": true, "default": ref},
		"prompt_prefix_file": map[string]any{"type": "object", "x-c2j-artifact-file": true},
		"token":              map[string]any{"type": "string", "x-secret": true, "default": "${{ context.secrets.token }}"},
		"checkpoint":         map[string]any{"type": "object", "x-c2j-object-type": "test.session/v1"},
		"message":            map[string]any{"type": "string", "default": "${{ context.config.?greeting.orValue('fallback') }}"},
		"legacy_path":        map[string]any{"type": "string"},
		"bulk":               map[string]any{"type": "string"},
		"unrelated":          map[string]any{"type": "object"},
	}}
	artifactFileManifest(t, repo, trace, "", map[string]any{"input_schema": schema, "env": map[string]string{"C2J_ARTIFACT_FILE_PROBE": "1", "C2J_ARTIFACT_FILE_TRACE": trace, "C2J_ARTIFACT_CHECK_TOKEN": "1"}})
	provider := &testSecretProvider{}
	rt := toyruntime.New(toyruntime.WithSecretProvider(provider))
	_, err := rt.SetTenantConfigurationValue(celltest.Context(context.Background()), jobdb.SetTenantConfigurationValueRequest{TenantID: "file-tests", Name: "greeting", Value: json.RawMessage(`"configured"`)})
	require.NoError(t, err)
	source := `id: artifact-files
input_schema: {sample: {type: string, required: true}}
inputs: {sample: '${{ inputs.sample }}'}
perms: {jobdb: {secrets: {resolve: [token]}}}
sequence:
 - id: checkpoint
   op: file_checkpoint
 - id: consume
   op: ./op
   artifacts:
    legacy:
     kind: value
     format: text
     value: legacy content
   inputs:
    checkpoint: '${{ sequence.checkpoint.outputs.checkpoint }}'
    prompt_prefix_file: '${{ context.artifacts["empty"] }}'
    legacy_path: '${{ context.environment.inbox }}/legacy'
    bulk: '${{ jq(inputs.sample, ". * 10") }}'
    unrelated: {kind: value, format: text, value: unannotated}
outputs: {result: '${{ sequence.consume.outputs }}'}
`
	sample := strings.Repeat("unrelated compact data λ", 1000)
	key := submitArtifactFileRecipe(t, rt, repo, source, "file-tests", "", map[string]any{"sample": sample}, jobdb.NewArtifactFromBytes("empty", nil))
	out, err := runGitLifecycle(t, rt, key)
	require.NoError(t, err)
	result := gitLifecycleOutput(t, out)["result"].(map[string]any)
	assertFileDigest(t, result, "instructions_file", body)
	assertFileDigest(t, result, "prompt_prefix_file", nil)
	require.Equal(t, "configured", result["message"])
	require.Equal(t, true, result["checkpoint_ok"])
	require.Equal(t, true, result["legacy_ok"])
	records := fileTrace(t, trace)
	require.Len(t, records, 1)
	require.Equal(t, strings.Repeat(sample, 10), records[0]["bulk"])
	require.Equal(t, map[string]any{"kind": "value", "format": "text", "value": "unannotated"}, records[0]["unrelated"])
	assertFileCleanup(t, records)
	durable := assertFileDurableValues(t, rt, key, "artifact-files-", "token-private-value", string(body))
	require.Contains(t, durable, "c2j-invocation-input.json")
	require.Equal(t, [][]string{{"token"}}, provider.Calls())
}
