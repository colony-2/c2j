package template

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveTemplate_Simple(t *testing.T) {
	recipeCtx := newRecipeCtx(t, nil)
	seqCtx := newSequenceCtx(t, recipeCtx, "test-seq", map[string]interface{}{
		"name": "Alice",
		"age":  30,
	})

	tests := []struct {
		name     string
		template string
		expected interface{}
	}{
		{
			name:     "simple input reference",
			template: "${{ inputs.name }}",
			expected: "Alice",
		},
		{
			name:     "no template",
			template: "static text",
			expected: "static text",
		},
		{
			name:     "CEL string concatenation",
			template: `${{ "Hello " + inputs.name }}`,
			expected: "Hello Alice",
		},
		{
			name:     "CEL arithmetic",
			template: "${{ inputs.age + 10 }}",
			expected: int64(40),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := seqCtx.resolveTemplate(tt.template)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestResolveTemplate_SequenceReferences(t *testing.T) {
	recipeCtx := newRecipeCtx(t, nil)
	seqCtx := newSequenceCtx(t, recipeCtx, "test", map[string]interface{}{})

	addOpOutput(t, seqCtx, "fetch", map[string]interface{}{
		"status": 200,
		"body":   map[string]interface{}{"data": "test"},
	})
	addOpOutput(t, seqCtx, "transform", map[string]interface{}{
		"result": "processed",
		"count":  5,
	})

	tests := []struct {
		name     string
		template string
		expected interface{}
	}{
		{
			name:     "sequence node output",
			template: "${{ sequence.fetch.outputs.status }}",
			expected: int64(200),
		},
		{
			name:     "nested sequence output",
			template: "${{ sequence.transform.outputs.result }}",
			expected: "processed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := seqCtx.resolveTemplate(tt.template)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestResolveTemplate_CELFunction(t *testing.T) {
	recipeCtx := newRecipeCtx(t, nil)
	seqCtx := newSequenceCtx(t, recipeCtx, "test", map[string]interface{}{})

	addOpOutput(t, seqCtx, "calc", map[string]interface{}{
		"value": 10,
	})

	result, err := seqCtx.resolveTemplate(`${{ sequence.calc.outputs.value + 5 }}`)
	require.NoError(t, err)
	assert.Equal(t, int64(15), result)
}

func TestResolveTemplate_JSONParse(t *testing.T) {
	inputs := map[string]interface{}{
		"config_json":  `{"enabled":true,"threshold":2,"nested":{"name":"demo"},"items":[{"id":1},{"id":2}]}`,
		"invalid_json": "{bad",
		"flag":         true,
	}
	recipeCtx := newRecipeCtx(t, inputs)
	seqCtx := newSequenceCtx(t, recipeCtx, "test", inputs)

	result, err := seqCtx.resolveTemplate("${{ json_parse(inputs.config_json) }}")
	require.NoError(t, err)

	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, resultMap["enabled"])
	assert.Equal(t, float64(2), resultMap["threshold"])

	nested, ok := resultMap["nested"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "demo", nested["name"])

	items, ok := resultMap["items"].([]interface{})
	require.True(t, ok)
	require.Len(t, items, 2)

	firstItem, ok := items[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(1), firstItem["id"])

	_, err = seqCtx.resolveTemplate("${{ json_parse(inputs.invalid_json) }}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "json_parse: invalid JSON")

	_, err = seqCtx.resolveTemplate("${{ json_parse(\"\") }}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "json_parse: expected string")

	_, err = seqCtx.resolveTemplate("${{ json_parse(inputs.flag) }}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "json_parse: expected string")
}

func TestCELJQFunctions(t *testing.T) {
	payload := map[string]interface{}{
		"user": map[string]interface{}{
			"id":   123,
			"name": "Ada",
		},
		"tags": []interface{}{"alpha", "beta"},
	}
	recipeCtx := newRecipeCtx(t, map[string]interface{}{
		"payload": payload,
		"raw":     "hello",
		"bad":     math.Inf(1),
	})
	seqCtx := newSequenceCtx(t, recipeCtx, "test", recipeCtx.TemplateData.ContainerInputs)

	t.Run("happy path selects nested field", func(t *testing.T) {
		val, err := seqCtx.resolveTemplate("${{ jq(inputs.payload, \".user.name\") }}")
		require.NoError(t, err)
		assert.Equal(t, "Ada", val)
	})

	t.Run("multiple results returns list", func(t *testing.T) {
		val, err := seqCtx.resolveTemplate("${{ jq(inputs.payload, \".tags[]\") }}")
		require.NoError(t, err)

		list, ok := val.([]interface{})
		require.True(t, ok)
		assert.ElementsMatch(t, []interface{}{"alpha", "beta"}, list)
	})

	t.Run("empty result yields nil", func(t *testing.T) {
		val, err := seqCtx.resolveTemplate("${{ jq(inputs.payload, \"empty\") }}")
		require.NoError(t, err)
		t.Logf("empty result type=%T value=%v", val, val)
		assert.Nil(t, val)
	})

	t.Run("invalid expression surfaces error", func(t *testing.T) {
		_, err := seqCtx.resolveTemplate("${{ jq(inputs.payload, \".user | \") }}")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "jq: invalid expression")
	})

	t.Run("non-map input still works", func(t *testing.T) {
		val, err := seqCtx.resolveTemplate("${{ jq(inputs.raw, \".\") }}")
		require.NoError(t, err)
		assert.Equal(t, "hello", val)
	})

	t.Run("json_stringify encodes map", func(t *testing.T) {
		val, err := seqCtx.resolveTemplate("${{ json_stringify(inputs.payload) }}")
		require.NoError(t, err)

		var decoded map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(val.(string)), &decoded))
		assert.Equal(t, float64(123), decoded["user"].(map[string]interface{})["id"])
	})

	t.Run("json_stringify errors on unsupported", func(t *testing.T) {
		_, err := seqCtx.resolveTemplate("${{ json_stringify(inputs.bad) }}")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "json_stringify: failed to encode JSON")
	})

	t.Run("string overload encodes map", func(t *testing.T) {
		val, err := seqCtx.resolveTemplate("${{ string(inputs.payload) }}")
		require.NoError(t, err)

		var decoded map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(val.(string)), &decoded))
		assert.Equal(t, "Ada", decoded["user"].(map[string]interface{})["name"])
	})

	t.Run("interpolation converts map to json", func(t *testing.T) {
		val, err := seqCtx.resolveTemplate("Payload: ${{ inputs.payload }}")
		require.NoError(t, err)

		s := val.(string)
		assert.Contains(t, s, "Payload: ")

		jsonPart := strings.TrimPrefix(s, "Payload: ")
		var decoded map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(jsonPart), &decoded))
	})
}

func assertResolvedTime(t *testing.T, value interface{}, expected time.Time) {
	t.Helper()
	switch v := value.(type) {
	case time.Time:
		assert.True(t, v.Equal(expected))
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, v)
		require.NoError(t, err)
		assert.True(t, parsed.Equal(expected))
	default:
		t.Fatalf("unexpected time value type %T", value)
	}
}

func TestEvaluateCEL_Conditions(t *testing.T) {
	recipeCtx := newRecipeCtx(t, nil)
	seqCtx := newSequenceCtx(t, recipeCtx, "test", map[string]interface{}{})

	addOpOutput(t, seqCtx, "validate", map[string]interface{}{
		"valid": true,
		"score": 0.9,
	})

	tests := []struct {
		name       string
		expression string
		expected   bool
	}{
		{
			name:       "simple boolean check",
			expression: "sequence.validate.outputs.valid == true",
			expected:   true,
		},
		{
			name:       "numeric comparison",
			expression: "sequence.validate.outputs.score > 0.8",
			expected:   true,
		},
		{
			name:       "combined condition",
			expression: "sequence.validate.outputs.valid && sequence.validate.outputs.score > 0.5",
			expected:   true,
		},
		{
			name:       "empty expression is true",
			expression: "",
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
}

func TestAddSequenceNode_WithRuns(t *testing.T) {
	recipeCtx := newRecipeCtx(t, nil)
	seqCtx := newSequenceCtx(t, recipeCtx, "test", map[string]interface{}{})

	addOpOutput(t, seqCtx, "retry_node", map[string]interface{}{
		"attempt": 1,
		"status":  "failed",
	})

	node := seqCtx.TemplateData.Sequence["retry_node"]
	assert.Equal(t, 1, node.Outputs["attempt"])
	assert.Len(t, node.Runs, 0)

	addOpOutput(t, seqCtx, "retry_node", map[string]interface{}{
		"attempt": 2,
		"status":  "success",
	})

	node = seqCtx.TemplateData.Sequence["retry_node"]
	assert.Equal(t, 2, node.Outputs["attempt"])
	assert.Equal(t, "success", node.Outputs["status"])
	assert.Len(t, node.Runs, 1)
	assert.Equal(t, 1, node.Runs[0].Outputs["attempt"])

	// Runs should be addressable from CEL expressions
	val, err := seqCtx.resolveTemplate("${{ sequence.retry_node.runs[0].outputs.status }}")
	require.NoError(t, err)
	assert.Equal(t, "failed", val)
}

func TestAddStateOutput(t *testing.T) {
	recipeCtx := newRecipeCtx(t, nil)
	smCtx := newStateMachineCtx(t, recipeCtx, "test-sm", map[string]interface{}{})

	addStateOutput(t, smCtx, "validate", map[string]interface{}{
		"valid":    true,
		"metadata": map[string]interface{}{"source": "api"},
	})

	state := smCtx.TemplateData.States["validate"]
	assert.True(t, state.Outputs["valid"].(bool))
	assert.Equal(t, "api", state.Outputs["metadata"].(map[string]interface{})["source"])
}

func TestNewChildContext(t *testing.T) {
	recipeCtx := newRecipeCtx(t, map[string]interface{}{
		"child_input": "value",
	})
	smCtx := newStateMachineCtx(t, recipeCtx, "parent-sm", recipeCtx.TemplateData.ContainerInputs)

	addStateOutput(t, smCtx, "previous_state", map[string]interface{}{
		"result": "done",
	})

	child := newStateCtx(t, smCtx, "child")

	assert.Equal(t, smCtx, child.Parent)
	assert.Equal(t, "value", child.TemplateData.ContainerInputs["child_input"])
	assert.Equal(t, "done", child.TemplateData.States["previous_state"].Outputs["result"])

	// Scope metadata should be initialized
	assert.NotEmpty(t, child.TemplateData.Scope.ExecutionID)
	assert.False(t, child.TemplateData.Scope.Timestamp.IsZero())
}

func TestEffectiveConstPropagation(t *testing.T) {
	recipeCtx := newRecipeCtx(t, nil)
	require.False(t, recipeCtx.EffectiveConst)

	constSeq, err := recipeCtx.NewChildContext(ScopeSequence, recipe.NodeMetadata{ID: "checks", Const: true}, "", map[string]interface{}{})
	require.NoError(t, err)
	assert.True(t, constSeq.EffectiveConst)

	constOp, err := constSeq.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "lint"}, "lint", nil)
	require.NoError(t, err)
	assert.True(t, constOp.EffectiveConst)

	explicitFalseChild, err := constSeq.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "test", Const: false}, "test", nil)
	require.NoError(t, err)
	assert.True(t, explicitFalseChild.EffectiveConst)

	mutableSeq := newSequenceCtx(t, recipeCtx, "mutable", map[string]interface{}{})
	assert.False(t, mutableSeq.EffectiveConst)

	stateMachine, err := recipeCtx.NewChildContext(ScopeStateMachine, recipe.NodeMetadata{ID: "sm", Const: true}, "", map[string]interface{}{})
	require.NoError(t, err)
	state := newStateCtx(t, stateMachine, "validate")
	assert.True(t, state.EffectiveConst)
}

func TestResolveValue_Recursive(t *testing.T) {
	recipeCtx := newRecipeCtx(t, nil)
	seqCtx := newSequenceCtx(t, recipeCtx, "test", map[string]interface{}{
		"base_url": "https://api.example.com",
		"version":  "v1",
	})

	addOpOutput(t, seqCtx, "auth", map[string]interface{}{
		"token": "abc123",
	})

	value := map[string]interface{}{
		"url": `${{ inputs.base_url + "/" + inputs.version }}`,
		"headers": map[string]interface{}{
			"Authorization": `${{ "Bearer " + sequence.auth.outputs.token }}`,
			"Content-Type":  "application/json",
		},
		"options": []interface{}{
			"${{ inputs.version }}",
			"stable",
		},
	}

	result, err := seqCtx.resolveValue(value)
	require.NoError(t, err)

	resultMap := result.(map[string]interface{})
	assert.Equal(t, "https://api.example.com/v1", resultMap["url"])

	headers := resultMap["headers"].(map[string]interface{})
	assert.Equal(t, "Bearer abc123", headers["Authorization"])
	assert.Equal(t, "application/json", headers["Content-Type"])

	options := resultMap["options"].([]interface{})
	assert.Equal(t, "v1", options[0])
	assert.Equal(t, "stable", options[1])
}

func TestValidateTemplateReferences(t *testing.T) {
	seqCtx := newSequenceCtx(t, newRecipeCtx(t, nil), "test", map[string]interface{}{})

	tests := []struct {
		name      string
		template  string
		wantError bool
	}{
		{
			name:      "valid template",
			template:  "${{ inputs.name }}",
			wantError: false,
		},
		{
			name:      "invalid syntax",
			template:  "${{ inputs.name",
			wantError: false, // Not a template anymore, just a plain string
		},
		{
			name:      "not a template",
			template:  "plain text",
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := seqCtx.validateTemplateReferences(tt.template)
			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateCELExpression(t *testing.T) {
	seqCtx := newSequenceCtx(t, newRecipeCtx(t, nil), "test", map[string]interface{}{})

	tests := []struct {
		name      string
		expr      string
		wantError bool
	}{
		{
			name:      "valid expression",
			expr:      "inputs.count > 0",
			wantError: false,
		},
		{
			name:      "invalid syntax",
			expr:      "inputs.count >",
			wantError: true,
		},
		{
			name:      "empty is valid",
			expr:      "",
			wantError: false,
		},
		{
			name:      "true is valid",
			expr:      "true",
			wantError: false,
		},
		{
			name:      "unqualified outputs not allowed",
			expr:      "outputs.value",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := seqCtx.validateCELExpression(tt.expr)
			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestStateTransitionContext(t *testing.T) {
	recipeCtx := newRecipeCtx(t, nil)
	smCtx := newStateMachineCtx(t, recipeCtx, "sm", map[string]interface{}{})
	stateCtx := newStateCtx(t, smCtx, "process")
	seqCtx := newSequenceCtx(t, stateCtx, "process-seq", smCtx.TemplateData.ContainerInputs)

	addOpOutput(t, seqCtx, "validate", map[string]interface{}{
		"valid": true,
	})
	addOpOutput(t, seqCtx, "transform", map[string]interface{}{
		"success": true,
		"count":   10,
	})

	tests := []struct {
		name       string
		expression string
		expected   bool
	}{
		{
			name:       "check sequence node output",
			expression: "sequence.transform.outputs.success == true",
			expected:   true,
		},
		{
			name:       "check multiple sequence nodes",
			expression: "sequence.validate.outputs.valid && sequence.transform.outputs.count > 5",
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
}

func TestComplexStateMachineScenario(t *testing.T) {
	recipeCtx := newRecipeCtx(t, map[string]interface{}{
		"user_id": "123",
		"payload": map[string]interface{}{"data": "test"},
	})
	smCtx := newStateMachineCtx(t, recipeCtx, "workflow", recipeCtx.TemplateData.ContainerInputs)

	addStateOutput(t, smCtx, "validate", map[string]interface{}{
		"valid":    true,
		"metadata": map[string]interface{}{"checked_at": time.Now()},
	})

	processState := newStateCtx(t, smCtx, "process")
	processSeq := newSequenceCtx(t, processState, "process-seq", smCtx.TemplateData.ContainerInputs)

	template := "${{ states.validate.outputs.valid }}"
	result, err := processState.resolveTemplate(template)
	require.NoError(t, err)
	assert.Equal(t, true, result)

	addOpOutput(t, processSeq, "enrich", map[string]interface{}{
		"enriched_data": map[string]interface{}{
			"user":  "${{ inputs.user_id }}",
			"extra": "info",
		},
	})

	template = "${{ sequence.enrich.outputs.enriched_data }}"
	result, err = processSeq.resolveTemplate(template)
	require.NoError(t, err)
	assert.NotNil(t, result)

	addStateOutput(t, smCtx, "process", map[string]interface{}{
		"final_result": "completed",
	})

	finalCtx := newStateCtx(t, smCtx, "complete")

	template = "${{ states.process.outputs.final_result }}"
	result, err = finalCtx.resolveTemplate(template)
	require.NoError(t, err)
	assert.Equal(t, "completed", result)
}

func TestScopeVisibility_Positive(t *testing.T) {
	recipeCtx := newRecipeCtx(t, map[string]interface{}{
		"sm_input": "parent-value",
	})
	smCtx := newStateMachineCtx(t, recipeCtx, "sm", recipeCtx.TemplateData.ContainerInputs)
	addStateOutput(t, smCtx, "prev", map[string]interface{}{
		"status": "ok",
	})

	stateCtx := newStateCtx(t, smCtx, "process")
	seqCtx := newSequenceCtx(t, stateCtx, "process-seq", smCtx.TemplateData.ContainerInputs)
	addOpOutput(t, seqCtx, "task", map[string]interface{}{
		"value": "done",
	})

	// State can see previous states
	val, err := stateCtx.resolveTemplate("${{ states.prev.outputs.status }}")
	require.NoError(t, err)
	assert.Equal(t, "ok", val)

	// Sequence can see parent inputs and state outputs
	val, err = seqCtx.resolveTemplate("${{ inputs.sm_input }}")
	require.NoError(t, err)
	assert.Equal(t, "parent-value", val)

	val, err = seqCtx.resolveTemplate("${{ sequence.task.outputs.value }}")
	require.NoError(t, err)
	assert.Equal(t, "done", val)

	// Op created under sequence inherits same visibility
	opCtx, err := seqCtx.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "inner-op"}, "inner-op", nil)
	require.NoError(t, err)
	val, err = opCtx.resolveTemplate("${{ inputs.sm_input }}")
	require.NoError(t, err)
	assert.Equal(t, "parent-value", val)

	// Other state can see completed state outputs
	anotherState := newStateCtx(t, smCtx, "next")
	val, err = anotherState.resolveTemplate("${{ states.prev.outputs.status }}")
	require.NoError(t, err)
	assert.Equal(t, "ok", val)
}

func TestScopeVisibility_Negative(t *testing.T) {
	recipeCtx := newRecipeCtx(t, map[string]interface{}{
		"foo": "bar",
	})

	// Child sequence should not see parent sequence nodes
	parentSeq := newSequenceCtx(t, recipeCtx, "parent-seq", recipeCtx.TemplateData.ContainerInputs)
	addOpOutput(t, parentSeq, "outer", map[string]interface{}{
		"val": 1,
	})
	childSeq := newSequenceCtx(t, parentSeq, "child-seq", parentSeq.TemplateData.ContainerInputs)
	_, err := childSeq.resolveTemplate("${{ sequence.outer.outputs.val }}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such key")

	// State B's sequence should not see State A's sequence nodes
	smCtx := newStateMachineCtx(t, recipeCtx, "sm", recipeCtx.TemplateData.ContainerInputs)
	stateA := newStateCtx(t, smCtx, "stateA")
	seqA := newSequenceCtx(t, stateA, "seqA", smCtx.TemplateData.ContainerInputs)
	addOpOutput(t, seqA, "taskA", map[string]interface{}{
		"value": "secret",
	})

	stateB := newStateCtx(t, smCtx, "stateB")
	seqB := newSequenceCtx(t, stateB, "seqB", smCtx.TemplateData.ContainerInputs)
	_, err = seqB.resolveTemplate("${{ sequence.taskA.outputs.value }}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such key")

	// Root (outside state machine) cannot see inside state machine states
	addStateOutput(t, smCtx, "stateA", map[string]interface{}{"result": "hidden"})
	_, err = recipeCtx.resolveTemplate("${{ states.stateA.outputs.result }}")
	require.Error(t, err)

	// Root (outside sequence) cannot see inside sequence nodes
	_, err = recipeCtx.resolveTemplate("${{ sequence.outer.outputs.val }}")
	require.Error(t, err)
}

func TestResolveTemplate_ContextWorkflowJobID(t *testing.T) {
	commitCtx := &contextual.GitCommitContext{}
	jobCtx := contextual.JobContext{
		Workflow: contextual.WorkflowContext{
			JobID: "job-12345",
		},
	}

	recipeCtx, err := NewRecipeResolutionContext(commitCtx, nil, jobCtx)
	require.NoError(t, err)

	result, err := recipeCtx.resolveTemplate("${{ context.workflow.job_id }}")
	require.NoError(t, err)
	assert.Equal(t, "job-12345", result)

	// Test in a sequence context - should inherit from parent
	seqCtx := newSequenceCtx(t, recipeCtx, "test-seq", map[string]interface{}{})
	result, err = seqCtx.resolveTemplate("${{ context.workflow.job_id }}")
	require.NoError(t, err)
	assert.Equal(t, "job-12345", result)

	// Test in a state machine context
	smCtx := newStateMachineCtx(t, recipeCtx, "test-sm", map[string]interface{}{})
	result, err = smCtx.resolveTemplate("${{ context.workflow.job_id }}")
	require.NoError(t, err)
	assert.Equal(t, "job-12345", result)
}
