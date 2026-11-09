package execution

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRepositoryFallbackIndependentScalarsAndCapabilities(t *testing.T) {
	ptr := func(s string) *string { return &s }
	defaults := Requirements{Base: ptr("linux*"), Capabilities: []string{"docker"}, Resources: Resources{CPU: ptr("8"), Memory: ptr("8Gi"), EphemeralStorage: ptr("10Gi")}}
	scope := Requirements{Resources: Resources{Memory: ptr("1Gi")}}
	got, err := RepositoryFallback(defaults, scope)
	require.NoError(t, err)
	require.Equal(t, "1Gi", *got.Resources.Memory)
	require.Equal(t, "8", *got.Resources.CPU)
	require.Equal(t, "10Gi", *got.Resources.EphemeralStorage)
	require.Equal(t, []string{"docker"}, got.Capabilities)
	scope.CapabilitiesDeclared = true
	got, err = RepositoryFallback(defaults, scope)
	require.NoError(t, err)
	require.Empty(t, got.Capabilities)
	scope.Capabilities = []string{"recipe"}
	got, err = RepositoryFallback(defaults, scope)
	require.NoError(t, err)
	require.Equal(t, []string{"recipe"}, got.Capabilities)
	scope = Requirements{Capabilities: []string{"job"}}
	got, err = RepositoryFallback(defaults, scope)
	require.NoError(t, err)
	require.Equal(t, []string{"docker", "job"}, got.Capabilities)
	_, err = RepositoryFallback(defaults, Requirements{Platform: ptr("linux/amd64")})
	require.ErrorContains(t, err, "legacy")
}

func TestSubmissionRetainsJobRequirementsWithPresence(t *testing.T) {
	s, err := Initial(nil, true, Requirements{Capabilities: []string{"job"}})
	require.NoError(t, err)
	raw, err := json.Marshal(s)
	require.NoError(t, err)
	var again Submission
	require.NoError(t, json.Unmarshal(raw, &again))
	require.Equal(t, s.JobRequirements, again.JobRequirements)
	for _, r := range []Requirements{{CapabilitiesDeclared: true}, {Capabilities: []string{"outer"}}} {
		raw, err = json.Marshal(r)
		require.NoError(t, err)
		var restored Requirements
		require.NoError(t, json.Unmarshal(raw, &restored))
		require.Equal(t, r, restored)
	}
}
