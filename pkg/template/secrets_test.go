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

func TestSecretFallbackTemplates(t *testing.T) {
	rc, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{})
	require.NoError(t, err)
	single, err := rc.ResolveInputs(map[string]any{"token": "${{ context.secrets.?token.orValue('public') }}"})
	require.NoError(t, err)
	require.Equal(t, secrets.WithDefault(secrets.Reference{Name: "token"}, "public"), single["token"])
	for _, fallback := range []string{"", "public", "line\nquoted\""} {
		literal, err := json.Marshal(fallback)
		require.NoError(t, err)
		expr := "${{ context.secrets.?token.orValue(" + string(literal) + ") }}"
		input, err := rc.ResolveInputs(map[string]any{"token": expr})
		require.NoError(t, err)
		require.Equal(t, secrets.WithDefault(secrets.Reference{Name: "token"}, fallback), input["token"])
		_, err = rc.ResolveMap(map[string]any{"value": expr})
		require.Error(t, err)
	}
	for _, expr := range []string{
		`${{ context.secrets.?token.orValue(inputs.value) }}`,
		`${{ context.secrets.?token.orValue(context.secrets.other) }}`,
		`${{ context.secrets.?token.orValue(123) }}`,
		`${{ context.secrets.?token.orValue("a" + "b") }}`,
		`prefix ${{ context.secrets.?token.orValue("") }}`,
		`${{ context.secrets.?token.orValue("") == "" }}`,
	} {
		_, err := rc.ResolveInputs(map[string]any{"token": expr})
		require.Error(t, err, expr)
	}
}

func TestSecretFallbackInternalsRemainOpaque(t *testing.T) {
	for _, ref := range []any{
		secrets.WithDefault(secrets.Reference{Name: "token"}, "public"),
		map[string]any{"$c2j_secret": "token", "default": "public"},
	} {
		rc, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, map[string]any{"token": ref}, contextual.JobContext{})
		require.NoError(t, err)
		for _, expr := range []string{`${{ inputs.token.default }}`, `{{ inputs.token.default }}`, `${{ inputs.token.default == "public" }}`} {
			_, err := rc.ResolveInputs(map[string]any{"value": expr})
			require.Error(t, err, expr)
		}
	}
}
