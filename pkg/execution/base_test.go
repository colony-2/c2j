package execution

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestBaseMatchingGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/base_matches.json")
	require.NoError(t, err)
	var cases []struct {
		Selector, Actual string
		Match            bool
	}
	require.NoError(t, json.Unmarshal(raw, &cases))
	for _, tc := range cases {
		t.Run(tc.Selector+"/"+tc.Actual, func(t *testing.T) {
			m, err := Compare(Requirements{Base: &tc.Selector}, Allocation{SchemaVersion: SchemaVersion, Base: tc.Actual})
			require.NoError(t, err)
			require.Equal(t, tc.Match, len(m) == 0)
			if !tc.Match {
				require.Equal(t, "base", m[0].Field)
				reason := "incompatible"
				if tc.Actual == "" {
					reason = "unknown_allocation"
				}
				require.Equal(t, reason, m[0].Reason)
				require.Equal(t, tc.Actual, m[0].Allocated)
			}
		})
	}
}

func TestBaseValidationAndOverlay(t *testing.T) {
	for _, bad := range []string{"", "has space", "tab\t", "line\n", "cr\r", "nul\x00", "\xff", strings.Repeat("a", 4097)} {
		_, err := (Requirements{Base: &bad}).Normalize()
		require.Error(t, err, "%q", bad)
		if bad != "" {
			_, err = (Allocation{SchemaVersion: SchemaVersion, Base: bad}).Normalize()
			require.Error(t, err, "%q", bad)
		}
	}
	_, err := (Allocation{SchemaVersion: SchemaVersion, Base: "macos*"}).Normalize()
	require.ErrorContains(t, err, "concrete")
	parent := Requirements{Base: ptr("macos*")}
	patch := Requirements{Base: ptr("ubuntu-26*arm64")}
	got, err := Overlay(parent, patch).Normalize()
	require.NoError(t, err)
	require.Equal(t, *patch.Base, *got.Base)
	*got.Base = "*"
	require.Equal(t, "ubuntu-26*arm64", *patch.Base)
	require.Equal(t, "macos*", *Overlay(parent, Requirements{}).Base)
	for _, legacy := range []Requirements{{Image: ptr("alpine")}, {Platform: ptr("linux/arm64")}} {
		_, err = Overlay(parent, legacy).Normalize()
		require.ErrorContains(t, err, "cannot be combined")
		_, err = Initial(&legacy, true, parent)
		require.ErrorContains(t, err, "cannot be combined")
	}
	for _, marshal := range []func(any) ([]byte, error){json.Marshal, yaml.Marshal} {
		raw, err := marshal(parent)
		require.NoError(t, err)
		var roundTrip Requirements
		require.NoError(t, yaml.Unmarshal(raw, &roundTrip))
		require.Equal(t, parent, roundTrip)
	}
}

func TestBaseDemandVersionsAndListing(t *testing.T) {
	d, err := Initial(&Requirements{Base: ptr("macos*")}, true, Requirements{})
	require.NoError(t, err)
	require.Equal(t, 4, d.SchemaVersion)
	raw, err := PayloadWithDemand(json.RawMessage(`{"other":9007199254740993}`), d.Demand)
	require.NoError(t, err)
	require.Contains(t, string(raw), "9007199254740993")
	v := Inspect(nil, raw)
	require.Empty(t, v.Diagnostic)
	require.Equal(t, "macos*", *v.Demand.Effective.Base)
	for _, tc := range []struct {
		base  string
		match bool
	}{{"macos-123-456", true}, {"ubuntu-26-arm64", false}, {"", false}} {
		match, err := (Filter{Allocation: Allocation{SchemaVersion: SchemaVersion, Base: tc.base}}).Match(v)
		require.NoError(t, err)
		require.Equal(t, tc.match, match)
	}
	d.SchemaVersion = 3
	require.ErrorContains(t, d.Validate(), "base requires schema version")
	d.Effective.Base = nil
	d.JobRequirements.Base = ptr("*")
	require.ErrorContains(t, d.Validate(), "base requires schema version")
	_, err = (Allocation{SchemaVersion: 1, Base: "macos"}).Normalize()
	require.ErrorContains(t, err, "base requires schema version")
	_, err = DecodeDemand([]byte(`{"schema_version":5,"recipe_resolution":"resolved","effective":{}}`))
	require.Error(t, err)
}

func TestLegacyImagePlatformRecordsRemainEnforced(t *testing.T) {
	d, err := DecodeDemand([]byte(`{"schema_version":3,"recipe_resolution":"resolved","effective":{"image":"alpine:3","platform":"linux/arm64"}}`))
	require.NoError(t, err)
	var a Allocation
	require.NoError(t, json.Unmarshal([]byte(`{"schema_version":1,"image":{"reference":"alpine:3"},"platform":"linux/arm64/v8"}`), &a))
	m, err := Compare(d.Effective, a)
	require.NoError(t, err)
	require.Empty(t, m)
	m, err = Compare(d.Effective, Allocation{SchemaVersion: 2, Base: "alpine-linux-arm64"})
	require.NoError(t, err)
	require.Len(t, m, 2, "a base cannot stand in for legacy allocation facts")
	m, err = Compare(Requirements{Base: ptr("macos*")}, a)
	require.NoError(t, err)
	require.Equal(t, "unknown_allocation", m[0].Reason)
	m, err = Compare(Requirements{Base: ptr("*")}, a)
	require.NoError(t, err)
	require.Empty(t, m)
	m, err = Compare(Requirements{Base: ptr("*"), Capabilities: []string{"docker"}}, Allocation{SchemaVersion: 2})
	require.NoError(t, err)
	require.Equal(t, "missing_capability", m[0].Reason)
}
