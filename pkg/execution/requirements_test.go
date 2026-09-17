package execution

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func ptr(s string) *string { return &s }

func TestQuantityNormalization(t *testing.T) {
	for _, tt := range []struct {
		input, want string
		cpu         bool
	}{
		{"1000m", "1", true}, {"0.5", "500m", true}, {"1.001", "1001m", true},
		{"0.001", "1m", true}, {"1.0000", "1", true},
		{"1024Mi", "1Gi", false}, {"1.5Gi", "1536Mi", false},
		{"1000k", "1M", false}, {"0.001k", "0.001k", false},
		{"0.5Ki", "0.512k", false}, {"1.001k", "1.001k", false},
		{"9223372036854775.807k", "9223372036854775.807k", false},
	} {
		t.Run(tt.input, func(t *testing.T) {
			fn := normalizeBytes
			if tt.cpu {
				fn = normalizeCPU
			}
			got, err := fn(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			again, err := fn(got)
			require.NoError(t, err)
			require.Equal(t, got, again, "normalization must be idempotent")
		})
	}
}

func TestInvalidQuantities(t *testing.T) {
	for _, s := range []string{"", "0", "-1", " 1", "1 ", "1e3", "1Gi", "0.1m", "0.0001", "1u", "9223372036854776", strings.Repeat("1", 129)} {
		_, err := normalizeCPU(s)
		require.Error(t, err, "CPU %q", s)
	}
	for _, s := range []string{"", "0Gi", "-1Gi", "1", "1B", "1KB", "1K", "1e3", "0.0001k", "0.0000000001Gi", "8Ei", "9223372036854775.808k", strings.Repeat("9", 100) + "Pi"} {
		_, err := normalizeBytes(s)
		require.Error(t, err, "bytes %q", s)
	}
}

func TestOverlayAndRoundTrip(t *testing.T) {
	base := Requirements{Image: ptr("alpine"), Platform: ptr("LINUX/AMD64"), Resources: Resources{CPU: ptr("2"), Memory: ptr("4Gi")}}
	patch := Requirements{Resources: Resources{Memory: ptr("1Gi"), EphemeralStorage: ptr("10Gi")}}
	got, err := Overlay(base, patch).Normalize()
	require.NoError(t, err)
	require.Equal(t, "1Gi", *got.Resources.Memory, "overrides can reduce requirements")
	require.Equal(t, "2", *got.Resources.CPU)
	require.Equal(t, "docker.io/library/alpine:latest", *got.Image)
	require.Equal(t, "linux/amd64", *got.Platform)
	*got.Resources.Memory = "2Gi"
	require.Equal(t, "1Gi", *patch.Resources.Memory)
	*got.Resources.CPU = "1"
	require.Equal(t, "2", *base.Resources.CPU)
	for _, codec := range []struct {
		marshal   func(any) ([]byte, error)
		unmarshal func([]byte, any) error
	}{{json.Marshal, json.Unmarshal}, {yaml.Marshal, yaml.Unmarshal}} {
		data, err := codec.marshal(got)
		require.NoError(t, err)
		var decoded Requirements
		require.NoError(t, codec.unmarshal(data, &decoded))
		require.Equal(t, got, decoded)
	}
	_, err = (Requirements{Image: ptr("")}).Normalize()
	require.Error(t, err)
	_, err = (Requirements{Resources: Resources{Memory: ptr("")}}).Normalize()
	require.Error(t, err)
}

func TestCompatibility(t *testing.T) {
	r := Requirements{Image: ptr("alpine:3"), Platform: ptr("linux/arm64"), Resources: Resources{CPU: ptr("1"), Memory: ptr("1Gi"), EphemeralStorage: ptr("2Gi")}}
	a := Allocation{SchemaVersion: SchemaVersion, Image: Image{Reference: "docker.io/library/alpine:3"}, Platform: ptr("LINUX/ARM64/v8"), Resources: Resources{CPU: ptr("1000m"), Memory: ptr("1024Mi"), EphemeralStorage: ptr("3Gi")}}
	mismatches, err := Compare(r, a)
	require.NoError(t, err)
	require.Empty(t, mismatches)
	mismatches, err = Compare(r, Allocation{SchemaVersion: SchemaVersion})
	require.NoError(t, err)
	require.Len(t, mismatches, 5)
	for _, m := range mismatches {
		require.Equal(t, "unknown_allocation", m.Reason)
	}
	a.Resources.Memory = ptr("512Mi")
	a.Platform = ptr("linux/amd64")
	mismatches, err = Compare(r, a)
	require.NoError(t, err)
	require.Len(t, mismatches, 2)
	_, err = Compare(r, Allocation{SchemaVersion: 99})
	require.Error(t, err)
	mismatches, err = Compare(Requirements{}, Allocation{SchemaVersion: SchemaVersion})
	require.NoError(t, err)
	require.Empty(t, mismatches, "unspecified requirements permit unknown allocation")
}

func TestImageIdentitiesAreDistinct(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	r := Requirements{Image: ptr("registry.example/runner@" + digest)}
	for _, a := range []Allocation{
		{SchemaVersion: SchemaVersion, Image: Image{ImageID: digest}},
		{SchemaVersion: SchemaVersion, Image: Image{Reference: *r.Image}},
	} {
		mismatches, err := Compare(r, a)
		require.NoError(t, err)
		require.Len(t, mismatches, 1, "only the actual manifest digest proves compatibility")
	}
	a := Allocation{SchemaVersion: SchemaVersion, Image: Image{Reference: "registry.example/runner:latest", ManifestDigest: digest, ImageID: "runtime://different-config-id"}}
	mismatches, err := Compare(r, a)
	require.NoError(t, err)
	require.Empty(t, mismatches)
	a.Image.Reference = "registry.example/runner@sha256:" + strings.Repeat("b", 64)
	_, err = a.Normalize()
	require.ErrorContains(t, err, "disagree")
	a.Image.Reference = ""
	a.Image.ManifestDigest = "sha256:bad"
	_, err = a.Normalize()
	require.Error(t, err)
	require.False(t, (Allocation{Image: Image{ImageID: digest}}).HasCompatibilityFacts())
}

func TestInvalidNames(t *testing.T) {
	for _, s := range []string{"", "linux", "linux/", "linux/amd64/v1/extra", "linux/ amd64"} {
		_, err := normalizePlatform(s)
		require.Error(t, err, s)
	}
	for _, s := range []string{"", "invalid image", "example.com/UPPERCASE", "alpine@sha256:bad"} {
		_, err := normalizeReference(s)
		require.Error(t, err, s)
	}
}
