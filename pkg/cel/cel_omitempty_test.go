package cel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"
)

func TestCELExpr_YAML_OmitEmpty_DoesNotDropNonEmptyExpression(t *testing.T) {
	type wrapper struct {
		When CELExpr `yaml:"when,omitempty"`
	}

	orig, err := NewCELExpr("inputs.a == 1")
	require.NoError(t, err)

	b, err := yamlv3.Marshal(wrapper{When: *orig})
	require.NoError(t, err)
	require.Contains(t, string(b), "when:")

	var out wrapper
	require.NoError(t, yamlv3.Unmarshal(b, &out))
	require.Equal(t, "inputs.a == 1", out.When.String())
}

func TestCELExpr_JSON_OmitEmpty_DoesNotDropNonEmptyExpression(t *testing.T) {
	type wrapper struct {
		When CELExpr `json:"when,omitempty"`
	}

	orig, err := NewCELExpr("inputs.count > 0")
	require.NoError(t, err)

	b, err := json.Marshal(wrapper{When: *orig})
	require.NoError(t, err)
	require.True(t, strings.Contains(string(b), `"when"`), "expected JSON to include when field, got: %s", string(b))

	var out wrapper
	require.NoError(t, json.Unmarshal(b, &out))
	require.Equal(t, "inputs.count > 0", out.When.String())
}
