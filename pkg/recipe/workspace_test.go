package recipe

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestWorkspaceRecipeRoundTripAndSchema(t *testing.T) {
	text := `id: workspaces
version: "1"
workspace: {cell: B, ref: "{{ inputs.ref }}"}
sequence:
 - id: nested
   workspace: {cell: C}
   state:
    initial: inspect
    states:
     inspect:
      workspace: {cell: B}
      sequence: []
`
	var rec Recipe
	require.NoError(t, yaml.Unmarshal([]byte(text), &rec))
	require.True(t, HasWorkspaces(rec))
	require.NoError(t, Validate(text))
	raw, err := yaml.Marshal(&rec)
	require.NoError(t, err)
	var round Recipe
	require.NoError(t, yaml.Unmarshal(raw, &round))
	require.Equal(t, rec.GetMetadata().Workspace, round.GetMetadata().Workspace)
	before, err := ExecutionDigest(rec)
	require.NoError(t, err)
	round.GetMetadata().Workspace.Cell = "other"
	after, err := ExecutionDigest(round)
	require.NoError(t, err)
	require.NotEqual(t, before, after)
}
func TestWorkspaceRejectsInvalidSpec(t *testing.T) {
	for _, spec := range []string{"null", "{}", "B", "{cell: ''}", "{cell: '   '}", "{cell: 9}", "{cell: B, ref: false}", "{cell: B, extra: true}", "{cell: B, cell: C}"} {
		t.Run(spec, func(t *testing.T) {
			var r Recipe
			require.Error(t, yaml.Unmarshal([]byte("workspace: "+spec+"\nsequence: []"), &r))
		})
	}
	var r Recipe
	err := yaml.Unmarshal([]byte("sequence:\n - shared: x\n   workspace: {cell: B}"), &r)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "shared definition"))
}
