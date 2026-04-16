package cel

import (
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCELExpressionEvaluation tests CEL expression evaluation with various scenarios
func TestCELExpressionEvaluation(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		data       map[string]interface{}
		expected   bool
		expectErr  bool
	}{
		// Simple comparisons
		{
			name:       "simple greater than",
			expression: "score > 80",
			data:       map[string]interface{}{"score": 85},
			expected:   true,
		},
		{
			name:       "simple less than",
			expression: "score < 80",
			data:       map[string]interface{}{"score": 75},
			expected:   true,
		},
		{
			name:       "simple equality",
			expression: "status == 'approved'",
			data:       map[string]interface{}{"status": "approved"},
			expected:   true,
		},
		// Complex AND/OR conditions
		{
			name:       "complex AND condition true",
			expression: "data_size > 1000000 && priority == 'high'",
			data: map[string]interface{}{
				"data_size": 1500000,
				"priority":  "high",
			},
			expected: true,
		},
		{
			name:       "complex AND condition false",
			expression: "data_size > 1000000 && priority == 'high'",
			data: map[string]interface{}{
				"data_size": 500000,
				"priority":  "high",
			},
			expected: false,
		},
		{
			name:       "complex OR condition",
			expression: "score > 90 || valid == false",
			data: map[string]interface{}{
				"score": 85,
				"valid": false,
			},
			expected: true,
		},
		// Nested object access
		{
			name:       "nested object access",
			expression: "user.role == 'admin' && user.active == true",
			data: map[string]interface{}{
				"user": map[string]interface{}{
					"role":   "admin",
					"active": true,
				},
			},
			expected: true,
		},
		// List operations
		{
			name:       "list contains",
			expression: "'read' in permissions",
			data: map[string]interface{}{
				"permissions": []string{"read", "write", "delete"},
			},
			expected: true,
		},
		{
			name:       "list size check",
			expression: "size(items) > 2",
			data: map[string]interface{}{
				"items": []string{"a", "b", "c", "d"},
			},
			expected: true,
		},
		// String operations
		{
			name:       "string contains",
			expression: "message.contains('error')",
			data: map[string]interface{}{
				"message": "An error occurred",
			},
			expected: true,
		},
		{
			name:       "string starts with",
			expression: "path.startsWith('/api')",
			data: map[string]interface{}{
				"path": "/api/v1/users",
			},
			expected: true,
		},
		// Mathematical operations
		{
			name:       "mathematical expression",
			expression: "(score * 2) + bonus > 100",
			data: map[string]interface{}{
				"score": 45,
				"bonus": 15,
			},
			expected: true,
		},
		// Type checking
		{
			name:       "type checking",
			expression: "type(value) == string",
			data: map[string]interface{}{
				"value": "test",
			},
			expected: true,
		},
		// Comparison with parentheses
		{
			name:       "comparison with parentheses",
			expression: "(score > 80) == true",
			data: map[string]interface{}{
				"score": 85,
			},
			expected: true,
		},
		// Error cases
		{
			name:       "undefined variable",
			expression: "undefined_var > 10",
			data:       map[string]interface{}{},
			expectErr:  true,
		},
		{
			name:       "type mismatch",
			expression: "score + 'string'",
			data: map[string]interface{}{
				"score": 10,
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := evaluateCEL(tt.expression, tt.data)

			if tt.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// evaluateCEL evaluates a CEL expression with the given data
func evaluateCEL(expression string, data map[string]interface{}) (bool, error) {
	// Create CEL environment
	env, err := cel.NewEnv(
		cel.Variable("score", cel.IntType),
		cel.Variable("status", cel.StringType),
		cel.Variable("data_size", cel.IntType),
		cel.Variable("priority", cel.StringType),
		cel.Variable("valid", cel.BoolType),
		cel.Variable("user", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("permissions", cel.ListType(cel.StringType)),
		cel.Variable("items", cel.ListType(cel.StringType)),
		cel.Variable("message", cel.StringType),
		cel.Variable("path", cel.StringType),
		cel.Variable("bonus", cel.IntType),
		cel.Variable("value", cel.DynType),
	)
	if err != nil {
		return false, err
	}

	// Parse expression
	ast, issues := env.Parse(expression)
	if issues != nil && issues.Err() != nil {
		return false, issues.Err()
	}

	// Check expression
	checked, issues := env.Check(ast)
	if issues != nil && issues.Err() != nil {
		return false, issues.Err()
	}

	// Create program
	prg, err := env.Program(checked)
	if err != nil {
		return false, err
	}

	// Evaluate
	out, _, err := prg.Eval(data)
	if err != nil {
		return false, err
	}

	// Convert to bool
	result, ok := out.Value().(bool)
	if !ok {
		return false, nil
	}

	return result, nil
}

// TestCELWithStateContext tests CEL expressions in the context of state machine transitions
func TestCELWithStateContext(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		outputs    map[string]interface{}
		state      map[string]interface{}
		expected   string
	}{
		{
			name:       "transition based on score",
			expression: "outputs.score >= 80",
			outputs: map[string]interface{}{
				"score": 85,
			},
			expected: "approved",
		},
		{
			name:       "transition based on complex condition",
			expression: "outputs.score >= 80 && outputs.confidence > 0.9",
			outputs: map[string]interface{}{
				"score":      85,
				"confidence": 0.95,
			},
			expected: "high_confidence_approval",
		},
		{
			name:       "transition with state reference",
			expression: "outputs.retries < state.max_retries",
			outputs: map[string]interface{}{
				"retries": 2,
			},
			state: map[string]interface{}{
				"max_retries": 3,
			},
			expected: "retry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This would integrate with the actual state machine compiler
			// For now, we're testing the CEL expression evaluation logic
			data := map[string]interface{}{
				"outputs": tt.outputs,
				"state":   tt.state,
			}

			// In real implementation, this would be part of evaluateTransitions
			env, _ := cel.NewEnv(
				cel.Variable("outputs", cel.MapType(cel.StringType, cel.DynType)),
				cel.Variable("state", cel.MapType(cel.StringType, cel.DynType)),
			)

			ast, _ := env.Parse(tt.expression)
			checked, _ := env.Check(ast)
			prg, _ := env.Program(checked)

			out, _, err := prg.Eval(data)
			require.NoError(t, err)

			result, ok := out.Value().(bool)
			require.True(t, ok)
			assert.True(t, result)
		})
	}
}

// TestCELPerformance tests CEL expression evaluation performance
func TestCELPerformance(t *testing.T) {
	// Create a complex expression
	expression := "(score > 80 && status == 'active') || (priority == 'high' && deadline < 100)"

	data := map[string]interface{}{
		"score":    85,
		"status":   "active",
		"priority": "medium",
		"deadline": 50,
	}

	// Pre-compile the expression
	env, err := cel.NewEnv(
		cel.Variable("score", cel.IntType),
		cel.Variable("status", cel.StringType),
		cel.Variable("priority", cel.StringType),
		cel.Variable("deadline", cel.IntType),
	)
	require.NoError(t, err)

	ast, issues := env.Parse(expression)
	require.NoError(t, issues.Err())

	checked, issues := env.Check(ast)
	require.NoError(t, issues.Err())

	prg, err := env.Program(checked)
	require.NoError(t, err)

	// Run multiple evaluations
	iterations := 1000
	for i := 0; i < iterations; i++ {
		out, _, err := prg.Eval(data)
		require.NoError(t, err)

		result, ok := out.Value().(bool)
		require.True(t, ok)
		assert.True(t, result)
	}
}
