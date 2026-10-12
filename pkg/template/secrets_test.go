package template

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/stretchr/testify/require"
)

func TestSecretTemplatesAreOpaqueAndInputOnly(t *testing.T) {
	rc, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, map[string]any{"token": secrets.Reference{Name: "github_token"}, "normal": "ok"}, contextual.JobContext{})
	require.NoError(t, err)
	for _, expr := range []string{"${{ context.secrets.github_token }}", "${{ inputs.token }}", "{{ inputs.token }}"} {
		out, err := rc.ResolveInputs(map[string]any{"token": expr})
		require.NoError(t, err)
		require.Equal(t, secrets.Reference{Name: "github_token"}, out["token"])
		_, err = rc.ResolveMap(map[string]any{"token": expr})
		require.Error(t, err)
		b, err := json.Marshal(out)
		require.NoError(t, err)
		var wire map[string]any
		require.NoError(t, json.Unmarshal(b, &wire))
		require.True(t, secrets.Contains(wire))
	}
	for _, expr := range []string{
		"${{ inputs.token == inputs.token }}", "${{ has(inputs.token) }}", "${{ inputs }}", "${{ inputs['token'] }}",
		"${{ string(inputs.token) }}", "x${{ inputs.token }}", "{{ printf \"%v\" inputs.token }}",
		"${{ context.secrets[inputs.normal] }}", "${{ context.secrets }}", "${{ context.secrets.github_token + 'x' }}",
	} {
		t.Run(expr, func(t *testing.T) { _, err := rc.ResolveInputs(map[string]any{"value": expr}); require.Error(t, err) })
	}
	_, err = rc.EvaluateCEL("has(inputs.token)")
	require.Error(t, err)
	_, err = rc.ResolveInputs(map[string]any{"value": "${{ inputs.normal + '!' }}"})
	require.NoError(t, err)
}
