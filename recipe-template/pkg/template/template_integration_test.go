package template

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration_SequenceWithTemplates(t *testing.T) {
	// This test simulates a realistic sequence execution scenario
	// Set initial inputs
	inputs := map[string]interface{}{
		"api_url":     "https://api.example.com",
		"api_key":     "secret123",
		"user_id":     "user-456",
		"max_retries": 3,
	}
	recipeCtx := newRecipeCtx(t, inputs)
	ctx := newSequenceCtx(t, recipeCtx, "data-pipeline", inputs)

	// Simulate first node: fetch_data
	fetchOutputs := map[string]interface{}{
		"status": 200,
		"body": map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{"id": 1, "name": "Item 1"},
				map[string]interface{}{"id": 2, "name": "Item 2"},
			},
			"total": 2,
		},
	}
	addOpOutput(t, ctx, "fetch_data", fetchOutputs)

	// Test template resolution for next node inputs
	validateInputTemplate := map[string]interface{}{
		"data":           "${{ sequence.fetch_data.outputs.body }}",
		"expected_count": "${{ sequence.fetch_data.outputs.body.total }}",
		"user":           "${{ inputs.user_id }}",
	}

	resolvedInputs, err := ctx.resolveValue(validateInputTemplate)
	require.NoError(t, err)

	inputsMap := resolvedInputs.(map[string]interface{})
	assert.Equal(t, fetchOutputs["body"], inputsMap["data"])
	assert.Equal(t, int64(2), inputsMap["expected_count"])
	assert.Equal(t, "user-456", inputsMap["user"])

	// Simulate second node: validate
	validateOutputs := map[string]interface{}{
		"valid":             true,
		"validation_errors": []interface{}{},
	}
	addOpOutput(t, ctx, "validate", validateOutputs)

	// Simulate third node: transform
	transformOutputs := map[string]interface{}{
		"processed_items": []interface{}{
			map[string]interface{}{"id": 1, "name": "ITEM 1", "processed": true},
			map[string]interface{}{"id": 2, "name": "ITEM 2", "processed": true},
		},
		"metadata": map[string]interface{}{
			"processing_time_ms": 125,
			"success":            true,
		},
	}
	addOpOutput(t, ctx, "transform", transformOutputs)

	// Test output mapping templates
	outputTemplates := map[string]interface{}{
		"items":         "${{ sequence.transform.outputs.processed_items }}",
		"is_valid":      "${{ sequence.validate.outputs.valid }}",
		"total_count":   "${{ sequence.fetch_data.outputs.body.total }}",
		"processing_ms": "${{ sequence.transform.outputs.metadata.processing_time_ms }}",
	}

	finalOutputs, err := ctx.resolveValue(outputTemplates)
	require.NoError(t, err)

	outputsMap := finalOutputs.(map[string]interface{})
	assert.Equal(t, transformOutputs["processed_items"], outputsMap["items"])
	assert.Equal(t, true, outputsMap["is_valid"])
	assert.Equal(t, int64(2), outputsMap["total_count"])
	assert.Equal(t, int64(125), outputsMap["processing_ms"])
}

func TestIntegration_StateMachineWithNestedSequence(t *testing.T) {
	// Create state machine context
	inputs := map[string]interface{}{
		"order_id":    "order-789",
		"customer_id": "cust-123",
		"amount":      99.99,
	}
	recipeCtx := newRecipeCtx(t, inputs)
	smCtx := newStateMachineCtx(t, recipeCtx, "order-workflow", inputs)

	// Execute validate state
	addStateOutput(t, smCtx, "validate_order", map[string]interface{}{
		"valid": true,
		"validation_details": map[string]interface{}{
			"credit_check":    "passed",
			"inventory_check": "passed",
		},
	})

	// Create process state context (nested sequence)
	processCtx := newStateCtx(t, smCtx, "process_order")
	processSeq := newSequenceCtx(t, processCtx, "process-seq", smCtx.TemplateData.ContainerInputs)

	// Test that process state can access validate state outputs
	template := "${{ states.validate_order.outputs.validation_details.credit_check }}"
	result, err := processCtx.resolveTemplate(template)
	require.NoError(t, err)
	assert.Equal(t, "passed", result)

	// Execute nodes within process state sequence
	addOpOutput(t, processSeq, "reserve_inventory", map[string]interface{}{
		"reservation_id": "res-001",
		"items_reserved": 2,
	})

	addOpOutput(t, processSeq, "charge_payment", map[string]interface{}{
		"transaction_id": "txn-456",
		"status":         "success",
		"charged_amount": 99.99,
	})

	addOpOutput(t, processSeq, "generate_invoice", map[string]interface{}{
		"invoice_id": "inv-789",
		"pdf_url":    "https://invoices.example.com/inv-789.pdf",
	})

	// Test process state output mapping with access to sequence nodes
	processOutputTemplate := map[string]interface{}{
		"transaction_id": "${{ sequence.charge_payment.outputs.transaction_id }}",
		"invoice_id":     "${{ sequence.generate_invoice.outputs.invoice_id }}",
		"reservation_id": "${{ sequence.reserve_inventory.outputs.reservation_id }}",
	}

	processOutputs, err := processSeq.resolveValue(processOutputTemplate)
	require.NoError(t, err)

	processOutputsMap := processOutputs.(map[string]interface{})
	assert.Equal(t, "txn-456", processOutputsMap["transaction_id"])
	assert.Equal(t, "inv-789", processOutputsMap["invoice_id"])
	assert.Equal(t, "res-001", processOutputsMap["reservation_id"])

	// Add process state outputs to parent context
	addStateOutput(t, smCtx, "process_order", processOutputsMap)

	// Test transition evaluation with sequence node access
	transitionExpr := "sequence.charge_payment.outputs.status == \"success\""
	// For transition evaluation, we need to temporarily add sequence to context
	tempCtx := &ResolutionContext{
		ScopeType:    smCtx.ScopeType,
		TemplateData: smCtx.TemplateData,
		CELEnv:       processSeq.CELEnv,
	}
	tempCtx.TemplateData.Sequence = processSeq.TemplateData.Sequence

	shouldTransition, err := tempCtx.EvaluateCEL(transitionExpr)
	require.NoError(t, err)
	assert.True(t, shouldTransition)

	// Execute complete state
	completeCtx := newStateCtx(t, smCtx, "complete")

	// Complete state can access all previous states
	completeTemplate := map[string]interface{}{
		"order_id":       "${{ inputs.order_id }}",
		"transaction_id": "${{ states.process_order.outputs.transaction_id }}",
		"invoice_id":     "${{ states.process_order.outputs.invoice_id }}",
		"status":         "completed",
	}

	completeOutputs, err := completeCtx.resolveValue(completeTemplate)
	require.NoError(t, err)

	completeOutputsMap := completeOutputs.(map[string]interface{})
	assert.Equal(t, "order-789", completeOutputsMap["order_id"])
	assert.Equal(t, "txn-456", completeOutputsMap["transaction_id"])
	assert.Equal(t, "inv-789", completeOutputsMap["invoice_id"])
	assert.Equal(t, "completed", completeOutputsMap["status"])
}

func TestIntegration_RetryScenario(t *testing.T) {
	// Test retry tracking with runs
	inputs := map[string]interface{}{
		"endpoint":    "https://flaky-api.example.com",
		"max_retries": 3,
	}
	recipeCtx := newRecipeCtx(t, inputs)
	ctx := newSequenceCtx(t, recipeCtx, "retry-workflow", inputs)

	// First attempt fails
	addOpOutput(t, ctx, "api_call", map[string]interface{}{
		"status":  500,
		"error":   "Internal Server Error",
		"attempt": 1,
	})

	// Second attempt fails
	addOpOutput(t, ctx, "api_call", map[string]interface{}{
		"status":  503,
		"error":   "Service Unavailable",
		"attempt": 2,
	})

	// Third attempt succeeds
	addOpOutput(t, ctx, "api_call", map[string]interface{}{
		"status":  200,
		"body":    map[string]interface{}{"result": "success"},
		"attempt": 3,
	})

	// Check that we can access runs
	node := ctx.TemplateData.Sequence["api_call"]
	assert.Equal(t, 200, node.Outputs["status"])
	assert.Len(t, node.Runs, 2) // Two previous attempts
	assert.Equal(t, 500, node.Runs[0].Outputs["status"])
	assert.Equal(t, 503, node.Runs[1].Outputs["status"])

	// Test template that references current output
	// Note: Accessing specific runs would need custom CEL functions
	result, err := ctx.resolveTemplate("${{ sequence.api_call.outputs.status }}")
	require.NoError(t, err)
	assert.Equal(t, int64(200), result)

	// Runs are addressable in CEL for audit history
	history, err := ctx.resolveTemplate("${{ sequence.api_call.runs[1].outputs.error }}")
	require.NoError(t, err)
	assert.Equal(t, "Service Unavailable", history)
}

func TestIntegration_ComplexCELExpressions(t *testing.T) {
	inputs := map[string]interface{}{
		"threshold":  100,
		"multiplier": 2,
	}
	recipeCtx := newRecipeCtx(t, inputs)
	smCtx := newStateMachineCtx(t, recipeCtx, "decision-workflow", inputs)
	seqCtx := newSequenceCtx(t, smCtx, "calc-seq", inputs)

	addOpOutput(t, seqCtx, "calculate", map[string]interface{}{
		"base_value":     50,
		"adjusted_value": 150,
		"metrics": map[string]interface{}{
			"count":   10,
			"average": 15.5,
		},
	})

	// Test complex CEL expressions
	tests := []struct {
		name       string
		expression string
		expected   bool
	}{
		{
			name:       "complex arithmetic",
			expression: "sequence.calculate.outputs.adjusted_value > inputs.threshold",
			expected:   true,
		},
		{
			name:       "nested field access",
			expression: "double(sequence.calculate.outputs.metrics.count) * sequence.calculate.outputs.metrics.average > 100.0",
			expected:   true,
		},
		{
			name:       "combined conditions",
			expression: "sequence.calculate.outputs.base_value * inputs.multiplier == 100 && sequence.calculate.outputs.adjusted_value > 0",
			expected:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := seqCtx.EvaluateCEL(tt.expression)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}

	// Test CEL expression
	template := `${{ sequence.calculate.outputs.base_value * 3 }}`
	result, err := seqCtx.resolveTemplate(template)
	require.NoError(t, err)
	assert.Equal(t, int64(150), result)

	// State runs tracking
	stateCtx := newStateCtx(t, smCtx, "loop-state")
	stateCtx.AddExecution(map[string]interface{}{"count": 1})
	stateCtx.AddExecution(map[string]interface{}{"count": 2})
	runCount, err := stateCtx.resolveTemplate(`${{ states["loop-state"].runs[0].outputs.count }}`)
	require.NoError(t, err)
	assert.Equal(t, int64(1), runCount)
}
