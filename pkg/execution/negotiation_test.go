package execution

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNegotiatePreservesEnclosingNeedsAndOverrides(t *testing.T) {
	ptr := func(v string) *string { return &v }
	d := Demand{SchemaVersion: DemandSchemaVersion, RecipeResolution: "resolved", Effective: Requirements{Base: ptr("macos*"), Resources: Resources{Memory: ptr("16Gi")}, Capabilities: []string{"network"}, Packages: []string{"uv:outer"}}}
	got, err := Negotiate(d, Requirements{Base: ptr("macos-123*"), Resources: Resources{Memory: ptr("8Gi")}, Capabilities: []string{"docker"}})
	require.NoError(t, err)
	require.Equal(t, "16Gi", *got.Effective.Resources.Memory)
	require.Equal(t, "macos-123*", *got.Effective.Base)
	require.Equal(t, []string{"docker", "network"}, got.Effective.Capabilities)
	require.Equal(t, []string{"uv:outer"}, got.Effective.Packages)
	_, err = Negotiate(d, Requirements{Base: ptr("ubuntu*")})
	require.ErrorContains(t, err, "enclosing base")
	d.Overrides.Resources.Memory = ptr("4Gi")
	_, err = Negotiate(d, Requirements{Resources: Resources{Memory: ptr("8Gi")}})
	require.ErrorContains(t, err, "job-wide resource override")
	d.Overrides = Requirements{Base: ptr("macos-456")}
	_, err = Negotiate(d, Requirements{Base: ptr("macos-123*")})
	require.ErrorContains(t, err, "job-wide base override")
}
