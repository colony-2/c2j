package template

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInterpolationIntegration tests the complete interpolation feature
func TestInterpolationIntegration(t *testing.T) {
	// Create a resolution context with test data
	inputs := map[string]interface{}{
		"user": map[string]interface{}{
			"name":     "Alice Smith",
			"id":       "USR-12345",
			"email":    "alice@example.com",
			"is_admin": false,
		},
		"order": map[string]interface{}{
			"id":     "ORD-98765",
			"amount": 299.99,
			"items":  3,
		},
		"environment": "production",
		"region":      "us-west-2",
		"api_version": "v2",
	}
	recipeCtx := newRecipeCtx(t, inputs)
	ctx := newSequenceCtx(t, recipeCtx, "integration-test", inputs)

	// Add sequence nodes
	addOpOutput(t, ctx, "validate", map[string]interface{}{
		"status": "valid",
		"score":  95,
	})

	addOpOutput(t, ctx, "process", map[string]interface{}{
		"result":    "success",
		"timestamp": "2024-01-15T10:30:00Z",
		"duration":  1234,
	})

	t.Run("Multi-expression interpolation", func(t *testing.T) {
		template := "User ${{ inputs.user.name }} (${{ inputs.user.id }}) placed order ${{ inputs.order.id }}"
		result, err := ctx.resolveTemplate(template)
		require.NoError(t, err)
		assert.Equal(t, "User Alice Smith (USR-12345) placed order ORD-98765", result)
	})

	t.Run("URL construction with interpolation", func(t *testing.T) {
		template := "https://api-${{ inputs.environment }}.example.com/${{ inputs.api_version }}/users/${{ inputs.user.id }}/orders/${{ inputs.order.id }}"
		result, err := ctx.resolveTemplate(template)
		require.NoError(t, err)
		assert.Equal(t, "https://api-production.example.com/v2/users/USR-12345/orders/ORD-98765", result)
	})

	t.Run("Log message with mixed types", func(t *testing.T) {
		template := "[${{ sequence.process.outputs.timestamp }}] Order ${{ inputs.order.id }} - ${{ inputs.order.items }} items totaling $${{ inputs.order.amount }} - Status: ${{ sequence.validate.outputs.status }}"
		result, err := ctx.resolveTemplate(template)
		require.NoError(t, err)
		assert.Equal(t, "[2024-01-15T10:30:00Z] Order ORD-98765 - 3 items totaling $299.99 - Status: valid", result)
	})

	t.Run("Complex nested data structure", func(t *testing.T) {
		input := map[string]interface{}{
			"notification": map[string]interface{}{
				"subject": "Order ${{ inputs.order.id }} Update",
				"body":    "Dear ${{ inputs.user.name }},\n\nYour order ${{ inputs.order.id }} has been ${{ sequence.process.outputs.result }}fully processed.\nTotal: $${{ inputs.order.amount }}\n\nProcessing time: ${{ sequence.process.outputs.duration }}ms",
				"metadata": map[string]interface{}{
					"user_id":  "${{ inputs.user.id }}",
					"order_id": "${{ inputs.order.id }}",
					"region":   "${{ inputs.region }}",
				},
			},
			"logs": []interface{}{
				"User ${{ inputs.user.id }} initiated order",
				"Validation score: ${{ sequence.validate.outputs.score }}",
				"Processing completed in ${{ sequence.process.outputs.duration }}ms",
			},
		}

		result, err := ctx.resolveValue(input)
		require.NoError(t, err)

		resultMap := result.(map[string]interface{})
		notification := resultMap["notification"].(map[string]interface{})
		assert.Equal(t, "Order ORD-98765 Update", notification["subject"])
		assert.Contains(t, notification["body"], "Dear Alice Smith")
		assert.Contains(t, notification["body"], "Your order ORD-98765 has been successfully processed")
		assert.Contains(t, notification["body"], "Total: $299.99")
		assert.Contains(t, notification["body"], "Processing time: 1234ms")

		metadata := notification["metadata"].(map[string]interface{})
		assert.Equal(t, "USR-12345", metadata["user_id"])
		assert.Equal(t, "ORD-98765", metadata["order_id"])
		assert.Equal(t, "us-west-2", metadata["region"])

		logs := resultMap["logs"].([]interface{})
		assert.Equal(t, "User USR-12345 initiated order", logs[0])
		assert.Equal(t, "Validation score: 95", logs[1])
		assert.Equal(t, "Processing completed in 1234ms", logs[2])
	})

	t.Run("Single expression returns raw type", func(t *testing.T) {
		// String
		result, err := ctx.resolveTemplate("${{ inputs.user.name }}")
		require.NoError(t, err)
		assert.Equal(t, "Alice Smith", result)

		// Number (float)
		result, err = ctx.resolveTemplate("${{ inputs.order.amount }}")
		require.NoError(t, err)
		assert.Equal(t, 299.99, result)

		// Number (int)
		result, err = ctx.resolveTemplate("${{ sequence.validate.outputs.score }}")
		require.NoError(t, err)
		assert.Equal(t, int64(95), result)

		// Boolean
		result, err = ctx.resolveTemplate("${{ inputs.user.is_admin }}")
		require.NoError(t, err)
		assert.Equal(t, false, result)

		// Map
		result, err = ctx.resolveTemplate("${{ inputs.user }}")
		require.NoError(t, err)
		userMap := result.(map[string]interface{})
		assert.Equal(t, "Alice Smith", userMap["name"])
		assert.Equal(t, "USR-12345", userMap["id"])
	})

	t.Run("CEL expressions in interpolation", func(t *testing.T) {
		// Arithmetic in interpolation
		template := "Order total with tax (10%): $${{ inputs.order.amount * 1.1 }}"
		result, err := ctx.resolveTemplate(template)
		require.NoError(t, err)
		// Use Contains for floating point result
		assert.Contains(t, result.(string), "Order total with tax (10%): $329.989")

		// Boolean logic in single expression
		template = "${{ inputs.order.items > 2 && sequence.validate.outputs.score > 90 }}"
		result, err = ctx.resolveTemplate(template)
		require.NoError(t, err)
		assert.Equal(t, true, result)

		// String concatenation still works
		template = `${{ "Order " + inputs.order.id + " for " + inputs.user.name }}`
		result, err = ctx.resolveTemplate(template)
		require.NoError(t, err)
		assert.Equal(t, "Order ORD-98765 for Alice Smith", result)
	})

	t.Run("Edge cases", func(t *testing.T) {
		// Empty expression
		result, err := ctx.resolveTemplate("${{ }}")
		require.NoError(t, err)
		assert.Equal(t, "", result)

		// No expressions
		result, err = ctx.resolveTemplate("Plain text with no expressions")
		require.NoError(t, err)
		assert.Equal(t, "Plain text with no expressions", result)

		// Expression with }} inside strings
		template := `${{ "This string has }} inside" }}`
		result, err = ctx.resolveTemplate(template)
		require.NoError(t, err)
		assert.Equal(t, "This string has }} inside", result)

		// Multiple }} in single quotes
		template = `${{ 'Another }} test }}' }}`
		result, err = ctx.resolveTemplate(template)
		require.NoError(t, err)
		assert.Equal(t, "Another }} test }}", result)

		// Whitespace handling
		template = "  ${{ inputs.user.name }}  "
		result, err = ctx.resolveTemplate(template)
		require.NoError(t, err)
		assert.Equal(t, "  Alice Smith  ", result)
	})
}

// TestPureCELModeIntegration tests that when conditions work correctly with pure CEL
func TestPureCELModeIntegration(t *testing.T) {
	inputs := map[string]interface{}{
		"retry_count": 2,
		"max_retries": 3,
		"status":      "pending",
	}
	recipeCtx := newRecipeCtx(t, inputs)
	stateMachineCtx := newStateMachineCtx(t, recipeCtx, "test-sm", inputs)
	seqCtx := newSequenceCtx(t, stateMachineCtx, "state-seq", inputs)

	addOpOutput(t, seqCtx, "validate", map[string]interface{}{
		"valid":  true,
		"errors": []interface{}{},
	})

	// Test pure CEL expressions for when conditions
	tests := []struct {
		name      string
		expr      string
		expected  bool
		expectErr bool
	}{
		{
			name:     "simple comparison",
			expr:     "inputs.retry_count < inputs.max_retries",
			expected: true,
		},
		{
			name:     "logical AND",
			expr:     `sequence.validate.outputs.valid == true && inputs.status == "pending"`,
			expected: true,
		},
		{
			name:     "logical OR",
			expr:     `inputs.retry_count >= inputs.max_retries || sequence.validate.outputs.valid == true`,
			expected: true,
		},
		{
			name:     "complex expression",
			expr:     `(inputs.retry_count < 3 && sequence.validate.outputs.valid) || inputs.status == "error"`,
			expected: true,
		},
		{
			name:      "invalid - string interpolation not allowed",
			expr:      "Status is ${{ inputs.status }}",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := seqCtx.EvaluateCEL(tt.expr)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
