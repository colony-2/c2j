package template

import (
	"encoding/json"
	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestConfigurationReferences(t *testing.T) {
	rc := newRecipeCtx(t, nil)
	for _, text := range []string{`${{ context.config.flag }}`, `{{ context.config.flag }}`, `${{ context.config["flag"] }}`} {
		got, err := rc.ResolveInputs(map[string]any{"value": text})
		require.NoError(t, err)
		require.Equal(t, configuration.Ref("flag"), got["value"])
		raw, err := json.Marshal(got)
		require.NoError(t, err)
		require.JSONEq(t, `{"value":{"$c2j_ref":{"kind":"config","name":"flag"}}}`, string(raw))
		_, err = rc.ResolveMap(map[string]any{"value": text})
		require.Error(t, err)
	}
}
func TestConfigurationCannotControlRouting(t *testing.T) {
	rc := newRecipeCtx(t, map[string]any{"flag": configuration.Ref("flag"), "normal": true})
	for _, expr := range []string{`context.config.flag`, `inputs.flag == true`, `has(inputs.flag)`, `inputs["flag"] == true`, `size(inputs)>0`} {
		_, err := rc.EvaluateCEL(expr)
		require.Error(t, err, expr)
	}
	for _, text := range []string{`${{ inputs.flag }}`, `{{ inputs.flag }}`, `{{ if inputs.flag }}yes{{ end }}`, `${{ context.config.flag == true }}`} {
		_, err := rc.ResolveMap(map[string]any{"value": text})
		require.Error(t, err, text)
	}
	got, err := rc.ResolveInputs(map[string]any{"value": `${{ inputs.flag }}`})
	require.NoError(t, err)
	require.Equal(t, configuration.Ref("flag"), got["value"])
	yes, err := rc.EvaluateCEL("inputs.normal")
	require.NoError(t, err)
	require.True(t, yes)
}

func TestWireConfigurationAliasesCannotBeInspected(t *testing.T) {
	var wire any
	require.NoError(t, json.Unmarshal([]byte(`{"$c2j_ref":{"kind":"config","name":"flag"}}`), &wire))
	rc := newRecipeCtx(t, map[string]any{"nested": map[string]any{"flag": wire}})
	for _, expr := range []string{`inputs.nested.flag["$c2j_ref"].kind == "config"`, `inputs.nested.flag == inputs.nested.flag`, `inputs["nested"]["flag"] != null`} {
		_, err := rc.EvaluateCEL(expr)
		require.Error(t, err, expr)
	}
	got, err := rc.ResolveInputs(map[string]any{"value": `${{ inputs.nested.flag }}`})
	require.NoError(t, err)
	require.Equal(t, wire, got["value"])
	for _, value := range []any{wire, `${{ inputs.nested.flag }}`, `${{ inputs.nested }}`} {
		_, err := rc.ResolveMap(map[string]any{"routing": value})
		require.Error(t, err)
	}
}

func TestConfigurationFallbacksStayDeferredAndCannotControlRouting(t *testing.T) {
	rc := newRecipeCtx(t, nil)
	for _, expr := range []string{`context.config.?flag.orValue(false)`, `context.config.?flag.orValue(null)`, `context.config[?"build.channel"].orValue("stable")`} {
		text := "${{ " + expr + " }}"
		got, err := rc.ResolveInputs(map[string]any{"value": text})
		require.NoError(t, err)
		ref, ok, err := configuration.Parse(got["value"])
		require.NoError(t, err)
		require.True(t, ok)
		require.NotEmpty(t, ref.Default)
		_, err = rc.ResolveMap(map[string]any{"value": text})
		require.Error(t, err)
		_, err = rc.EvaluateCEL(expr)
		require.Error(t, err)
		forwarded := newRecipeCtx(t, got)
		alias, err := forwarded.ResolveInputs(map[string]any{"alias": "${{ inputs.value }}"})
		require.NoError(t, err)
		require.Equal(t, ref, alias["alias"])
		_, err = forwarded.EvaluateCEL("inputs.value == false")
		require.Error(t, err)
	}
	for _, text := range []string{`${{ context.config.?flag.orValue(inputs.default) }}`, `${{ context.config.?flag.orValue(context.secrets.token) }}`, `value: ${{ context.config.?flag.orValue(false) }}`} {
		_, err := rc.ResolveInputs(map[string]any{"value": text})
		require.Error(t, err, text)
	}
}
