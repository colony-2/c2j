package cel

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"
)

func TestCELExpr_YAML_RoundTrip_PreservesExpression(t *testing.T) {
	type wrapper struct {
		When CELExpr `yaml:"when"`
	}

	orig, err := NewCELExpr("inputs.a == 1")
	require.NoError(t, err)

	in := wrapper{When: *orig}
	b, err := yamlv3.Marshal(in)
	require.NoError(t, err)

	var out wrapper
	require.NoError(t, yamlv3.Unmarshal(b, &out))
	assert.Equal(t, "inputs.a == 1", out.When.String())
}

func TestCELExpr_YAML_Unmarshal_DoesNotCompile(t *testing.T) {
	type wrapper struct {
		When CELExpr `yaml:"when"`
	}

	// This expression is intentionally invalid for the base CEL env used by pkg/cel/compile
	// (no "outputs" variable). Unmarshal must still preserve it as text.
	var out wrapper
	require.NoError(t, yamlv3.Unmarshal([]byte("when: \"outputs.exit_code == 0\"\n"), &out))
	assert.Equal(t, "outputs.exit_code == 0", out.When.String())

	// Evaluation should still fail later with a validation error.
	_, err := out.When.Evaluate(map[string]interface{}{})
	require.Error(t, err)
}

func TestCELExpr_JSON_RoundTrip_PreservesExpression(t *testing.T) {
	type wrapper struct {
		When CELExpr `json:"when"`
	}

	orig, err := NewCELExpr("inputs.count > 0")
	require.NoError(t, err)

	in := wrapper{When: *orig}
	b, err := json.Marshal(in)
	require.NoError(t, err)

	var out wrapper
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, "inputs.count > 0", out.When.String())
}

func TestCELExpr_JSON_Unmarshal_DoesNotCompile(t *testing.T) {
	type wrapper struct {
		When CELExpr `json:"when"`
	}

	var out wrapper
	require.NoError(t, json.Unmarshal([]byte(`{"when":"outputs.value"}`), &out))
	assert.Equal(t, "outputs.value", out.When.String())

	_, err := out.When.Evaluate(map[string]interface{}{})
	require.Error(t, err)
}
