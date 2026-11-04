package execution

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCapabilitiesNormalizeOverlayAndCompare(t *testing.T) {
	base := Requirements{Capabilities: []string{"vendor.example/feature-v2", "docker", "docker"}}
	patch := Requirements{Capabilities: []string{"docker.workspace-mounts", "docker"}}
	r, err := Overlay(base, patch).Normalize()
	require.NoError(t, err)
	require.Equal(t, []string{"docker", "docker.workspace-mounts", "vendor.example/feature-v2"}, r.Capabilities)
	require.False(t, r.Empty())
	require.Equal(t, r, Overlay(r, Requirements{Capabilities: []string{}}))
	a, err := (Allocation{SchemaVersion: 1, Capabilities: append(r.Capabilities, "extra")}).Normalize()
	require.NoError(t, err)
	require.True(t, a.HasCompatibilityFacts())
	mismatches, err := Compare(r, a)
	require.NoError(t, err)
	require.Empty(t, mismatches)
	for _, supplied := range [][]string{nil, {"docker.workspace-mounts"}, {"dockerish"}} {
		mismatches, err = Compare(Requirements{Capabilities: []string{"docker"}}, Allocation{SchemaVersion: 1, Capabilities: supplied})
		require.NoError(t, err)
		require.Equal(t, []Mismatch{{Field: "capabilities", Required: "docker", Reason: "missing_capability"}}, mismatches)
	}
	mismatches, err = Compare(r, Allocation{SchemaVersion: 1})
	require.NoError(t, err)
	require.Len(t, mismatches, 3)
	r.Capabilities[0] = "mutated"
	require.Equal(t, "vendor.example/feature-v2", base.Capabilities[0])
	require.Equal(t, "docker.workspace-mounts", patch.Capabilities[0])
	require.Contains(t, a.Capabilities, "docker")
}

func TestCapabilitiesValidationAndEncoding(t *testing.T) {
	for _, name := range []string{"", "Docker", " docker", "docker ", "1docker", "a..b", "a/", "a*b", "a:b", "a\nb"} {
		_, err := (Requirements{Capabilities: []string{name}}).Normalize()
		require.Error(t, err, name)
		_, err = (Allocation{SchemaVersion: 1, Capabilities: []string{name}}).Normalize()
		require.Error(t, err, name)
	}
	for _, raw := range []string{`{"capabilities":[true]}`, `{"capabilities":[12]}`, `{"capabilities":"docker"}`} {
		var r Requirements
		require.Error(t, json.Unmarshal([]byte(raw), &r))
	}
	for _, raw := range []string{"capabilities: [true]", "capabilities: [12]", "capabilities: [null]", "capabilities: docker", "capabilities: [Docker]"} {
		var r Requirements
		require.Error(t, yaml.Unmarshal([]byte(raw), &r), raw)
	}
	original := Requirements{Capabilities: []string{"unknown.feature", "vendor/device-v2"}}
	raw, err := json.Marshal(original)
	require.NoError(t, err)
	var decoded Requirements
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, original, decoded)
	raw, err = yaml.Marshal(original)
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(raw, &decoded))
	require.Equal(t, original, decoded)
}

// These literals deliberately retain the pre-capability encoding and version
// numbers. Do not generate them with the current structs or schema constants.
func TestPreCapabilityExecutionRecordsCompatibility(t *testing.T) {
	require.Equal(t, 4, DemandSchemaVersion)
	require.Equal(t, 2, SchemaVersion)
	metadata := json.RawMessage(`{"execution":{"schema_version":3,"recipe_resolution":"resolved","effective":{"resources":{"memory":"2Gi"}},"job_requirements":{"resources":{"memory":"2Gi"}}}}`)
	payload := json.RawMessage(`{"c2j":{"execution":{"schema_version":3,"recipe_resolution":"resolved","effective":{"resources":{"memory":"4Gi"}}}}}`)
	v := Inspect(metadata, payload)
	require.Equal(t, "specified", v.Status)
	require.Equal(t, "yield", v.Source)
	require.Empty(t, v.Diagnostic)
	require.Empty(t, v.Initial.JobRequirements.Capabilities)
	require.Empty(t, v.Demand.Effective.Capabilities)
	var a Allocation
	require.NoError(t, json.Unmarshal([]byte(`{"schema_version":1,"resources":{"memory":"8Gi"}}`), &a))
	a, err := a.Normalize()
	require.NoError(t, err)
	require.Empty(t, a.Capabilities)
	matched, err := (Filter{Allocation: a}).Match(v)
	require.NoError(t, err)
	require.True(t, matched)
	// Old durable directives and recipe declarations still overlay normally.
	var directive Requirements
	require.NoError(t, json.Unmarshal([]byte(`{"resources":{"memory":"8Gi"}}`), &directive))
	var root Requirements
	require.NoError(t, yaml.Unmarshal([]byte("resources: {memory: 2Gi}\n"), &root))
	effective, err := Overlay(root, directive).Normalize()
	require.NoError(t, err)
	require.Empty(t, effective.Capabilities)
	require.Equal(t, "8Gi", *effective.Resources.Memory)
	// Updating this same snapshot with capabilities needs no version migration.
	v.Demand.Effective = Overlay(v.Demand.Effective, Requirements{Capabilities: []string{"synthetic.worker"}})
	next, err := PayloadWithDemand(payload, *v.Demand)
	require.NoError(t, err)
	updated := Inspect(metadata, next)
	require.Equal(t, []string{"synthetic.worker"}, updated.Demand.Effective.Capabilities)
	require.Equal(t, 3, updated.Demand.SchemaVersion)
	matched, err = (Filter{Allocation: a}).Match(updated)
	require.NoError(t, err)
	require.False(t, matched, "old allocation records cannot establish new capabilities")
	for _, tc := range []struct{ raw, status string }{
		{`{"schema_version":3,"recipe_resolution":"resolved","effective":{}}`, "unspecified"},
		{`{"schema_version":3,"recipe_resolution":"unresolved","effective":{}}`, "unresolved"},
		{`{"schema_version":99,"recipe_resolution":"resolved","effective":{}}`, "unsupported"},
		{`{"schema_version":3,"recipe_resolution":"resolved","effective":{"capabilities":[true]}}`, "malformed"},
	} {
		view := Inspect(nil, json.RawMessage(`{"c2j":{"execution":`+tc.raw+`}}`))
		require.Equal(t, tc.status, view.Status)
	}
}
