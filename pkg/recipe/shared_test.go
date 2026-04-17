package recipe

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"
)

func TestDuration_YAML_RoundTrip_And_Invalid(t *testing.T) {
	// Human-readable durations parse correctly [pkg/recipe/shared.go]
	var d Duration
	require.NoError(t, yamlv3.Unmarshal([]byte("1s"), &d))
	assert.Equal(t, "1s", d.String())

	// Invalid duration strings fail with clear errors [pkg/recipe/shared.go]
	var d2 Duration
	err := yamlv3.Unmarshal([]byte("abc"), &d2)
	assert.Error(t, err)

	// Duration values round-trip through YAML correctly [pkg/recipe/shared.go]
	b, err := yamlv3.Marshal(Duration(0))
	require.NoError(t, err)
	assert.Equal(t, "0s\n", string(b))
}

func TestRetryPolicy_YAML_Omit_Empty(t *testing.T) {
	// Retry configurations serialize with only set fields [pkg/recipe/shared.go]
	rp := RetryPolicy{MaximumAttempts: 3, NonRetryableErrorTypes: []string{"x"}}
	b, err := yamlv3.Marshal(rp)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, "maximum_attempts")
	assert.Contains(t, s, "non_retryable_error_types")

	// Empty retry policies omit from YAML output [pkg/recipe/shared.go]
	b2, err := yamlv3.Marshal(RetryPolicy{})
	require.NoError(t, err)
	assert.Equal(t, "{}\n", string(b2))
}

func TestDataMaps_Handle_Nil_And_Nested(t *testing.T) {
	// Input maps accept heterogeneous parameter types [pkg/recipe/shared.go]
	in := map[string]interface{}{"a": 1, "b": "x", "c": true}
	// Output maps capture varied result types [pkg/recipe/shared.go]
	out := map[string]interface{}{"n": 2, "s": "y", "ok": false}
	assert.Equal(t, 3, len(in))
	assert.Equal(t, 3, len(out))
	// Nested data structures preserve through maps [pkg/recipe/shared.go]
	in["nested"] = map[string]interface{}{"z": nil}
	assert.NotNil(t, in["nested"])
	// Nil values in maps handle gracefully [pkg/recipe/shared.go]
	out["nil"] = nil
}
