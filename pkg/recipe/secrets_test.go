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
	meta.InputSchema["token"] = InputSchema{Type: "string", Secret: true, Default: "plaintext"}
	_, err = meta.ValidateInputShapeAndFillDefaults(nil)
	require.ErrorContains(t, err, "no default")
}
