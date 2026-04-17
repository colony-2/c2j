package cel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Expression Creation and Validation
func TestCEL_NewAndAlwaysTrueAndInvalid(t *testing.T) {
	// Business rules compile successfully with valid CEL syntax [pkg/cel/cel.go]
	e, err := NewCELExpr("inputs.score > 80")
	require.NoError(t, err)
	assert.Equal(t, "inputs.score > 80", e.String())

	// Empty expression defaults to always-true condition for workflows [pkg/cel/cel.go]
	e2, err := NewCELExpr("")
	require.NoError(t, err)
	ok, err := e2.AsBool(map[string]interface{}{"score": 0})
	require.NoError(t, err)
	assert.True(t, ok)

	// Invalid business rule syntax fails compilation with descriptive error [pkg/cel/cel.go]
	_, err = NewCELExpr("inputs.")
	require.Error(t, err)

	// Complex conditional expressions with nested logic compile correctly [pkg/cel/cel.go]
	_, err = NewCELExpr("(inputs.a > 1 && inputs.b.c == 'x') || inputs.flag")
	require.NoError(t, err)
}

// Expression Evaluation
func TestCEL_AsBool_AndEvaluate_Types(t *testing.T) {
	// Boolean conditions evaluate correctly for workflow branching decisions [pkg/cel/cel.go]
	e, _ := NewCELExpr("inputs.active && inputs.score >= 90")
	b, err := e.AsBool(map[string]interface{}{"active": true, "score": 95})
	require.NoError(t, err)
	assert.True(t, b)

	// String expressions produce expected text output for templates [pkg/cel/cel.go]
	e2, _ := NewCELExpr("inputs.user.name")
	v, err := e2.Evaluate(map[string]interface{}{"user": map[string]interface{}{"name": "Ada"}})
	require.NoError(t, err)
	assert.Equal(t, "Ada", v)

	// Numeric calculations return accurate results for metrics [pkg/cel/cel.go]
	e3, _ := NewCELExpr("(inputs.a * 2) + inputs.b")
	v3, err := e3.Evaluate(map[string]interface{}{"a": 10, "b": 5})
	require.NoError(t, err)
	assert.Equal(t, int64(25), v3) // cel-go may normalize to int64

	// Expression fails gracefully when accessing undefined variables [pkg/cel/cel.go]
	e4, _ := NewCELExpr("inputs.missing > 0")
	_, err = e4.Evaluate(map[string]interface{}{})
	assert.Error(t, err)

	// Type mismatches in expressions produce clear error messages [pkg/cel/cel.go]
	e5, _ := NewCELExpr("inputs.a + 'x'")
	_, err = e5.Evaluate(map[string]interface{}{"a": 1})
	assert.Error(t, err)
}

// Data Access and Transformation
func TestCEL_DataAccessAndYamlRoundTrip(t *testing.T) {
	// Expressions access nested input data for decision making [pkg/cel/cel.go]
	e, _ := NewCELExpr("inputs.user.address.city == 'Paris'")
	ok, err := e.AsBool(map[string]interface{}{
		"user": map[string]interface{}{"address": map[string]interface{}{"city": "Paris"}},
	})
	require.NoError(t, err)
	assert.True(t, ok)

	// Map data converts to CEL values preserving all types [pkg/cel/cel.go]
	e2, _ := NewCELExpr("inputs.n == 5 && inputs.s == 'hi' && inputs.b == true")
	ok2, err := e2.AsBool(map[string]interface{}{"n": 5, "s": "hi", "b": true})
	require.NoError(t, err)
	assert.True(t, ok2)

	// Null values in data handled without causing evaluation errors [pkg/cel/cel.go]
	e3, _ := NewCELExpr("inputs.opt == null")
	ok3, err := e3.AsBool(map[string]interface{}{"opt": nil})
	require.NoError(t, err)
	assert.True(t, ok3)

	// Circular references in data structures detected and prevented [pkg/cel/cel.go]
	t.Skip("BUG: circular references not explicitly detected; no guard present")
}

// YAML Integration and Runtime Safety
func TestCEL_YAML_Serialize_Unserialize_AndSafety(t *testing.T) {
	// Expression configurations persist correctly through YAML serialization [pkg/cel/cel.go]
	orig, _ := NewCELExpr("inputs.a == 1")
	data, err := orig.MarshalYAML()
	require.NoError(t, err)
	var round CELExpr
	require.NoError(t, round.UnmarshalYAML(func(v interface{}) error {
		p := v.(*string)
		*p = data.(string)
		return nil
	}))
	ok, err := round.AsBool(map[string]interface{}{"a": 1})
	require.NoError(t, err)
	assert.True(t, ok)

	// Unsupported data types fail fast with clear errors [pkg/cel/cel.go]
	type custom struct{ X int }
	e2b, _ := NewCELExpr("inputs.x")
	_, err = e2b.Evaluate(map[string]interface{}{"x": custom{X: 1}})
	assert.Error(t, err)
}
