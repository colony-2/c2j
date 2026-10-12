package recipe

import (
	"testing"

	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/stretchr/testify/require"
)

func TestSecretInputSchema(t *testing.T) {
	meta := RecipeMetadata{InputSchema: map[string]InputSchema{"token": {Type: "string", Secret: true, Required: true}, "normal": {Type: "string"}}}
	_, err := meta.ValidateInputShapeAndFillDefaults(map[string]any{"token": secrets.Reference{Name: "github_token"}})
	require.NoError(t, err)
	for _, v := range []map[string]any{{"token": "plaintext"}, {}, {"token": secrets.Reference{Name: "token"}, "normal": secrets.Reference{Name: "token"}}} {
		_, err := meta.ValidateInputShapeAndFillDefaults(v)
		require.Error(t, err)
	}
	meta.InputSchema["token"] = InputSchema{Type: "string", Secret: true, Default: 123}
	_, err = meta.ValidateInputShapeAndFillDefaults(nil)
	require.ErrorContains(t, err, "default must be a string")
}

func TestSecretRecipeDefaultsStayDeferred(t *testing.T) {
	meta := RecipeMetadata{InputSchema: map[string]InputSchema{"token": {Type: "string", Secret: true, Required: true, Default: "public"}}}
	for _, tc := range []struct {
		name  string
		input map[string]any
		want  secrets.Reference
	}{
		{"omitted", nil, secrets.WithDefault(secrets.Reference{}, "public")},
		{"named", map[string]any{"token": secrets.Reference{Name: "missing"}}, secrets.WithDefault(secrets.Reference{Name: "missing"}, "public")},
		{"explicit", map[string]any{"token": `${{ context.secrets.?missing.orValue("") }}`}, secrets.WithDefault(secrets.Reference{Name: "missing"}, "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := meta.ValidateInputShapeAndFillDefaults(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.want, got["token"])
			// Forwarding through another recipe does not replace the binding's fallback.
			other := RecipeMetadata{InputSchema: map[string]InputSchema{"token": {Type: "string", Secret: true, Default: "other"}}}
			forwarded, err := other.ValidateInputShapeAndFillDefaults(got)
			require.NoError(t, err)
			require.Equal(t, got, forwarded)
		})
	}
	_, err := meta.ValidateInputShapeAndFillDefaults(map[string]any{"token": "public"})
	require.ErrorContains(t, err, "not plaintext")
}
