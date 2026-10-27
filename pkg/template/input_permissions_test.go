package template

import (
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/stretchr/testify/require"
)

func TestForeignRecipeMayForwardInputWithoutAmbientSecretAuthority(t *testing.T) {
	rc, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, map[string]any{"token": secrets.Reference{Name: "foo"}}, contextual.JobContext{})
	require.NoError(t, err)
	rc.Permissions = permissions.State{Version: permissions.Version, Inputs: &permissions.InputPolicy{Foreign: true}}
	forwarded, err := rc.ResolveInputs(map[string]any{"token": "${{ inputs.token }}"})
	require.NoError(t, err)
	require.Equal(t, secrets.Reference{Name: "foo"}, forwarded["token"])
	for _, value := range []any{"${{ context.secrets.foo }}", map[string]any{secrets.WireKey: "foo"}} {
		_, err := rc.ResolveInputs(map[string]any{"token": value})
		require.ErrorContains(t, err, "lease permission denied")
	}
}
