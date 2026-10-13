package ops

import (
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/stretchr/testify/require"
)

func TestEmptyPreparationRetainsLegacyInvocationEncoding(t *testing.T) {
	const original = `{"workspace_managed":true,"input":{"run":"true"},"context":{"invoke_seq":0}}`
	for _, scopes := range [][]toolenv.Scope{nil, {}} {
		var req ActivityInvocationRequest
		require.NoError(t, json.Unmarshal([]byte(original), &req))
		req.Preparation.Scopes = scopes
		raw, err := json.Marshal(req)
		require.NoError(t, err)
		require.Equal(t, original, string(raw))
		require.Equal(t, sha256.Sum256([]byte(original)), sha256.Sum256(raw))
	}
}

func TestNonemptyPreparationRetainsIdentity(t *testing.T) {
	for _, prep := range []PreparationRequest{
		{Extension: &ExtensionResolutionRequest{Selector: "nix:github:example/ops#probe"}},
		{Scopes: []toolenv.Scope{{ID: "recipe", Packages: []string{"uv:black"}}}},
	} {
		raw, err := json.Marshal(ActivityInvocationRequest{Preparation: prep})
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &fields))
		require.Contains(t, fields, "preparation")
		var roundtrip ActivityInvocationRequest
		require.NoError(t, json.Unmarshal(raw, &roundtrip))
		require.Equal(t, prep, roundtrip.Preparation)
	}
}
