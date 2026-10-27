package ops

import (
	"testing"

	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/stretchr/testify/require"
)

func TestExtensionDefaultAuthorizationUsesRootRepository(t *testing.T) {
	for _, tc := range []struct {
		name, source                                                 string
		grant, includeAllowance, explicitInput, unknownRoot, allowed bool
	}{
		{name: "same root", source: "git+https://EXAMPLE.test/team/root.git//ops/tool@commit", allowed: true},
		{name: "foreign op", source: "git+https://example.test/team/foreign.git//ops/tool@commit"},
		{name: "foreign explicit grant", source: "git+https://example.test/team/foreign.git//ops/tool@commit", grant: true, allowed: true},
		{name: "include input allowance is not an op grant", source: "git+https://example.test/team/foreign.git//ops/tool@commit", includeAllowance: true},
		{name: "another explicit input cannot authorize default", source: "git+https://example.test/team/foreign.git//ops/tool@commit", explicitInput: true},
		{name: "unknown root", source: "git+https://example.test/team/root.git//ops/tool@commit", unknownRoot: true},
		{name: "unknown op", source: "nix:example#tool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := permissions.State{Version: permissions.Version, Inputs: &permissions.InputPolicy{RootRepository: "https://example.test/team/root"}}
			if tc.unknownRoot {
				policy.Inputs.RootRepository = ""
			}
			if tc.includeAllowance {
				policy.Inputs.Allowed = []string{"foo"}
			}
			if tc.grant {
				policy.Grant = &permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"foo"}}}
			}
			inv := ActivityInvocationRequest{Permissions: &policy}
			if tc.explicitInput {
				inv.InputSecrets = []string{"foo"}
			}
			err := authorizeDefaultSecrets(&inv, &extops.ResolvedOp{ResolvedSelector: tc.source}, secrets.Reference{Name: "foo"})
			if tc.allowed {
				require.NoError(t, err)
				require.Contains(t, inv.InputSecrets, "foo")
			} else {
				require.ErrorContains(t, err, "lease permission denied")
			}
			if !tc.grant {
				require.Nil(t, policy.Grant, "input authorization must not create a delegated grant")
			}
		})
	}
}
