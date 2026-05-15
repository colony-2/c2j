package template

import (
	"context"
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/template/funcregistry"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInterpolateString(t *testing.T) {
	recipeCtx := newRecipeCtx(t, nil)
	ctx := newSequenceCtx(t, recipeCtx, "test", map[string]interface{}{
		"name":     "Alice",
		"user_id":  123,
		"domain":   "example.com",
		"version":  "v2",
		"priority": "HIGH",
		"order_id": "ORD-456",
		"action":   "login",
		"status":   "active",
		"code":     "ABC",
	})

	addOpOutput(t, ctx, "fetch", map[string]interface{}{
		"status": 200,
		"body":   map[string]interface{}{"data": "test"},
	})
	addOpOutput(t, ctx, "check", map[string]interface{}{
		"valid":    true,
		"is_valid": true,
	})
	addOpOutput(t, ctx, "count", map[string]interface{}{
		"total": 42,
	})
	addOpOutput(t, ctx, "timer", map[string]interface{}{
		"duration": 1500,
	})
	readme := swf.NewArtifactFromBytes("readme.md", []byte("data"))
	swf.AssignArtifactKey(readme, swf.ArtifactKey{
		JobId:       "job",
		TaskOrdinal: 1,
		Name:        "readme.md",
		SizeBytes:   int64(len("data")),
	})
	readmeRef, err := recipeartifacts.RefFromArtifact(readme)
	require.NoError(t, err)
	ctx.TemplateData.Sequence["build"] = StepOutput{
		Outputs: map[string]interface{}{},
		Artifacts: map[string]recipeartifacts.Ref{
			"readme.md": readmeRef,
		},
	}

	tests := []struct {
		name     string
		template string
		mode     RenderMode
		expected interface{}
		isString bool // Whether result should be a string
	}{
		// Single expression tests (backward compatibility)
		{
			name:     "single expression returns raw type - string",
			template: "${{ inputs.name }}",
			mode:     ModeInterpolation,
			expected: "Alice",
			isString: true,
		},
		{
			name:     "single expression returns raw type - number",
			template: "${{ inputs.user_id }}",
			mode:     ModeInterpolation,
			expected: int64(123),
			isString: false,
		},
		{
			name:     "single expression returns raw type - boolean",
			template: "${{ sequence.check.outputs.valid }}",
			mode:     ModeInterpolation,
			expected: true,
			isString: false,
		},
		{
			name:     "single expression returns raw type - map",
			template: "${{ sequence.fetch.outputs.body }}",
			mode:     ModeInterpolation,
			expected: map[string]interface{}{"data": "test"},
			isString: false,
		},
		{
			name:     "single expression returns raw type - artifact",
			template: "${{ sequence.build.artifacts[\"readme.md\"] }}",
			mode:     ModeInterpolation,
			expected: readmeRef,
			isString: false,
		},

		// Interpolation tests
		{
			name:     "multiple expressions interpolation",
			template: "Hello ${{ inputs.name }}, your ID is ${{ inputs.user_id }}",
			mode:     ModeInterpolation,
			expected: "Hello Alice, your ID is 123",
			isString: true,
		},
		{
			name:     "URL construction",
			template: "https://${{ inputs.domain }}/api/${{ inputs.version }}/users/${{ inputs.user_id }}",
			mode:     ModeInterpolation,
			expected: "https://example.com/api/v2/users/123",
			isString: true,
		},
		{
			name:     "log message format",
			template: "[${{ inputs.priority }}] Order ${{ inputs.order_id }} - User ${{ inputs.user_id }} performed ${{ inputs.action }}",
			mode:     ModeInterpolation,
			expected: "[HIGH] Order ORD-456 - User 123 performed login",
			isString: true,
		},
		{
			name:     "mixed static and dynamic",
			template: "Order ${{ inputs.order_id }} status: ${{ inputs.status }}",
			mode:     ModeInterpolation,
			expected: "Order ORD-456 status: active",
			isString: true,
		},
		{
			name:     "processed items summary",
			template: "Processed ${{ sequence.count.outputs.total }} items in ${{ sequence.timer.outputs.duration }}ms",
			mode:     ModeInterpolation,
			expected: "Processed 42 items in 1500ms",
			isString: true,
		},

		// Plain text (no expressions)
		{
			name:     "plain text no markers",
			template: "Hello World",
			mode:     ModeInterpolation,
			expected: "Hello World",
			isString: true,
		},

		// CEL expressions in strings
		{
			name:     "CEL string concat still works",
			template: `${{ "Hello " + inputs.name }}`,
			mode:     ModeInterpolation,
			expected: "Hello Alice",
			isString: true,
		},
		{
			name:     "CEL arithmetic",
			template: "${{ inputs.user_id + 100 }}",
			mode:     ModeInterpolation,
			expected: int64(223),
			isString: false,
		},

		// Quotes in expressions
		{
			name:     "double quotes with }} inside",
			template: `${{ "text with }} inside" }}`,
			mode:     ModeInterpolation,
			expected: "text with }} inside",
			isString: true,
		},
		{
			name:     "single quotes with }} inside",
			template: `${{ 'text with }} inside' }}`,
			mode:     ModeInterpolation,
			expected: "text with }} inside",
			isString: true,
		},
		{
			name:     "mixed quotes in CEL",
			template: `${{ inputs.status == 'active' || inputs.code == "ABC" }}`,
			mode:     ModeInterpolation,
			expected: true,
			isString: false,
		},

		// Empty and whitespace
		{
			name:     "empty expression",
			template: "${{ }}",
			mode:     ModeInterpolation,
			expected: "",
			isString: true,
		},
		{
			name:     "whitespace preserved",
			template: "  ${{ inputs.name }}  ",
			mode:     ModeInterpolation,
			expected: "  Alice  ", // Multiple segments due to whitespace
			isString: true,
		},
		{
			name:     "whitespace preserved in interpolation",
			template: "  Hello ${{ inputs.name }}  ",
			mode:     ModeInterpolation,
			expected: "  Hello Alice  ",
			isString: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ctx.interpolateString(tt.template, tt.mode)
			require.NoError(t, err)

			if tt.isString {
				assert.IsType(t, "", result)
			}
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInterpolateString_Errors(t *testing.T) {
	ctx := newSequenceCtx(t, newRecipeCtx(t, nil), "test", map[string]interface{}{})
	readme := swf.NewArtifactFromBytes("readme.md", []byte("data"))
	swf.AssignArtifactKey(readme, swf.ArtifactKey{
		JobId:       "job",
		TaskOrdinal: 1,
		Name:        "readme.md",
		SizeBytes:   int64(len("data")),
	})
	readmeRef, err := recipeartifacts.RefFromArtifact(readme)
	require.NoError(t, err)
	ctx.TemplateData.Sequence["build"] = StepOutput{
		Outputs: map[string]interface{}{},
		Artifacts: map[string]recipeartifacts.Ref{
			"readme.md": readmeRef,
		},
	}

	tests := []struct {
		name      string
		template  string
		mode      RenderMode
		expectErr string
	}{
		{
			name:      "unclosed expression",
			template:  "Hello ${{ inputs.name",
			mode:      ModeInterpolation,
			expectErr: "template parse error",
		},
		{
			name:      "undefined field",
			template:  "${{ inputs.nonexistent }}",
			mode:      ModeInterpolation,
			expectErr: "no such key: nonexistent",
		},
		{
			name:      "invalid CEL syntax",
			template:  "${{ inputs.name + }}",
			mode:      ModeInterpolation,
			expectErr: "failed to compile CEL expression",
		},
		{
			name:      "multiple expressions with error",
			template:  "Hello ${{ inputs.name }}, ID: ${{ inputs.missing }}",
			mode:      ModeInterpolation,
			expectErr: "expression error at position",
		},
		{
			name:      "artifact in mixed interpolation",
			template:  "Artifact ${{ sequence.build.artifacts[\"readme.md\"] }}",
			mode:      ModeInterpolation,
			expectErr: "artifact values cannot be interpolated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ctx.interpolateString(tt.template, tt.mode)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectErr)
			assert.Nil(t, result)
		})
	}
}

func TestResolveValueWithMode(t *testing.T) {
	ctx := newSequenceCtx(t, newRecipeCtx(t, nil), "test", map[string]interface{}{
		"name":  "Alice",
		"count": 5,
	})

	tests := []struct {
		name     string
		input    interface{}
		mode     RenderMode
		expected interface{}
	}{
		{
			name:     "string interpolation",
			input:    "Hello ${{ inputs.name }}",
			mode:     ModeInterpolation,
			expected: "Hello Alice",
		},
		{
			name: "map with templates",
			input: map[string]interface{}{
				"greeting": "Hello ${{ inputs.name }}",
				"count":    "${{ inputs.count }}",
				"static":   "no template",
			},
			mode: ModeInterpolation,
			expected: map[string]interface{}{
				"greeting": "Hello Alice",
				"count":    int64(5),
				"static":   "no template",
			},
		},
		{
			name: "slice with templates",
			input: []interface{}{
				"${{ inputs.name }}",
				"Count: ${{ inputs.count }}",
				"static",
			},
			mode: ModeInterpolation,
			expected: []interface{}{
				"Alice",
				"Count: 5",
				"static",
			},
		},
		{
			name: "nested structure",
			input: map[string]interface{}{
				"user": map[string]interface{}{
					"name":    "${{ inputs.name }}",
					"message": "Hello ${{ inputs.name }}!",
				},
				"items": []interface{}{
					"Item ${{ inputs.count }}",
				},
			},
			mode: ModeInterpolation,
			expected: map[string]interface{}{
				"user": map[string]interface{}{
					"name":    "Alice",
					"message": "Hello Alice!",
				},
				"items": []interface{}{
					"Item 5",
				},
			},
		},
		{
			name:     "non-string types pass through",
			input:    42,
			mode:     ModeInterpolation,
			expected: 42,
		},
		{
			name:     "boolean pass through",
			input:    true,
			mode:     ModeInterpolation,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ctx.ResolveValueWithMode(tt.input, tt.mode)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestResolveValueWithMode_NestedMapOfMaps(t *testing.T) {
	// Test case for nested map resolution (map of maps)
	// Simulates the scenario where input object has nested structure:
	// type Foo struct { Bar Bar }
	// type Bar struct { MyString string }
	// And MyString contains a template like "${{ context.environment.worktree_path }}"

	recipeCtx := newRecipeCtx(t, nil)

	// Create parent context with some data to reference
	parentCtx := newSequenceCtx(t, recipeCtx, "parent", map[string]interface{}{
		"testValue": "hello",
	})
	addOpOutput(t, parentCtx, "op1", map[string]interface{}{
		"result": "test-result",
	})

	// Test 1: Simulate what happens during input resolution (ResolveMap)
	// This is what compiler.go does at line 160 for sequences and line 14 for state machines
	t.Run("resolveMap with nested structure using inputs reference", func(t *testing.T) {
		// Create raw inputs that would be passed to a sequence/state machine
		// These contain templates that reference parent context
		rawInputs := map[string]interface{}{
			"foo": map[string]interface{}{
				"bar": map[string]interface{}{
					"myString": "${{ inputs.testValue }}",
				},
			},
			"simpleField": "${{ sequence.op1.outputs.result }}",
		}

		// This simulates what happens in compiler.go when resolving inputs before creating child context
		resolved, err := parentCtx.ResolveMap(rawInputs)
		require.NoError(t, err)

		t.Logf("Resolved inputs: %+v", resolved)

		// Check that simple field was resolved
		simpleField := resolved["simpleField"]
		assert.IsType(t, "", simpleField)
		assert.Equal(t, "test-result", simpleField)

		// Check that nested map was resolved
		foo, ok := resolved["foo"]
		require.True(t, ok, "foo should exist in resolved map")

		fooMap, ok := foo.(map[string]interface{})
		require.True(t, ok, "foo should be a map")

		bar, ok := fooMap["bar"]
		require.True(t, ok, "bar should exist in foo map")

		barMap, ok := bar.(map[string]interface{})
		require.True(t, ok, "bar should be a map")

		myString, ok := barMap["myString"]
		require.True(t, ok, "myString should exist in bar map")

		// This is the critical assertion - the nested template should be resolved
		myStringValue, ok := myString.(string)
		require.True(t, ok, "myString should be a string")
		assert.NotContains(t, myStringValue, "${{", "nested template should be resolved")
		assert.Equal(t, "hello", myStringValue)
	})

	// Test 2: Now create a child context with those resolved inputs and verify they work
	t.Run("child context with resolved nested inputs", func(t *testing.T) {
		rawInputs := map[string]interface{}{
			"nested": map[string]interface{}{
				"config": map[string]interface{}{
					"value": "${{ inputs.testValue }}",
				},
			},
		}

		// Resolve inputs first (as compiler does)
		resolved, err := parentCtx.ResolveMap(rawInputs)
		require.NoError(t, err)

		// Create child context with resolved inputs
		childCtx := newSequenceCtx(t, parentCtx, "child", resolved)

		// Now when we reference the input in the child, it should already be resolved
		result, err := childCtx.resolveTemplate("${{ inputs.nested.config.value }}")
		require.NoError(t, err)
		assert.Equal(t, "hello", result)
	})

	// Test 3: Deeply nested structure with context fields
	t.Run("deeply nested structure with context reference", func(t *testing.T) {
		deeplyNested := map[string]interface{}{
			"level1": map[string]interface{}{
				"level2": map[string]interface{}{
					"level3": map[string]interface{}{
						"inputRef": "${{ inputs.testValue }}",
						"seqRef":   "${{ sequence.op1.outputs.result }}",
					},
				},
			},
		}

		resolved, err := parentCtx.ResolveMap(deeplyNested)
		require.NoError(t, err)

		// Navigate through the nested structure
		l1 := resolved["level1"].(map[string]interface{})
		l2 := l1["level2"].(map[string]interface{})
		l3 := l2["level3"].(map[string]interface{})

		inputRef := l3["inputRef"].(string)
		assert.Equal(t, "hello", inputRef)

		seqRef := l3["seqRef"].(string)
		assert.Equal(t, "test-result", seqRef)
	})

	// Test 4: Mixed nested and non-nested templates
	t.Run("mixed nested structure", func(t *testing.T) {
		mixed := map[string]interface{}{
			"topLevel": "${{ inputs.testValue }}",
			"nested": map[string]interface{}{
				"middle": "${{ sequence.op1.outputs.result }}",
				"deeper": map[string]interface{}{
					"value": "${{ inputs.testValue }}",
				},
			},
		}

		resolved, err := parentCtx.ResolveMap(mixed)
		require.NoError(t, err)

		assert.Equal(t, "hello", resolved["topLevel"])
		nested := resolved["nested"].(map[string]interface{})
		assert.Equal(t, "test-result", nested["middle"])
		deeper := nested["deeper"].(map[string]interface{})
		assert.Equal(t, "hello", deeper["value"])
	})
}

func TestResolveValueWithMode_NestedMapContextEnvironment(t *testing.T) {
	// This test specifically tests the user's reported scenario:
	// type Foo struct { Bar Bar }
	// type Bar struct { MyString string }
	// where MyString contains "${{ context.environment.worktree_path }}"

	// Note: The test helpers create a context with default/empty environment values
	// In a real scenario, the worktree_path would be populated
	recipeCtx := newRecipeCtx(t, nil)

	// Create a parent sequence with some inputs
	parentCtx := newSequenceCtx(t, recipeCtx, "parent", map[string]interface{}{
		"someValue": "test",
	})

	t.Run("nested map with context.environment.worktree_path", func(t *testing.T) {
		// Raw inputs that would be passed to a child sequence/state machine
		// This matches the user's reported structure: Foo.Bar.MyString
		rawInputs := map[string]interface{}{
			"foo": map[string]interface{}{
				"bar": map[string]interface{}{
					"myString": "${{ context.environment.worktree_path }}",
				},
			},
		}

		// Resolve the inputs (this is what compiler.go does before creating child context)
		resolved, err := parentCtx.ResolveMap(rawInputs)
		require.NoError(t, err)

		t.Logf("Resolved inputs: %+v", resolved)

		// Navigate to the nested value
		foo := resolved["foo"].(map[string]interface{})
		bar := foo["bar"].(map[string]interface{})
		myString := bar["myString"]

		// The template should be resolved (the value will be empty string in test, but should not contain ${{}})
		myStringStr, ok := myString.(string)
		require.True(t, ok, "myString should be a string")
		assert.NotContains(t, myStringStr, "${{", "template markers should be gone - nested map template should be resolved")

		// Log the actual value for debugging
		t.Logf("Resolved myString value: '%s'", myStringStr)
	})

	t.Run("create child context and access resolved nested input", func(t *testing.T) {
		// Raw inputs with nested template
		rawInputs := map[string]interface{}{
			"config": map[string]interface{}{
				"paths": map[string]interface{}{
					"worktree": "${{ context.environment.worktree_path }}",
				},
			},
		}

		// Resolve inputs first
		resolved, err := parentCtx.ResolveMap(rawInputs)
		require.NoError(t, err)

		// Create child context with resolved inputs
		childCtx := newSequenceCtx(t, parentCtx, "child", resolved)

		// Access the resolved value through the child context
		result, err := childCtx.resolveTemplate("${{ inputs.config.paths.worktree }}")
		require.NoError(t, err)

		// The result should be a string (even if empty) and not contain template markers
		resultStr, ok := result.(string)
		require.True(t, ok, "result should be a string")
		assert.NotContains(t, resultStr, "${{", "nested input should be resolved")

		t.Logf("Resolved worktree value: '%s'", resultStr)
	})
}

func TestResolveValueWithMode_CustomMapTypes(t *testing.T) {
	// Test case for custom map types like recipe.InputMap
	// Bug: When a value is recipe.InputMap (a type alias for map[string]interface{}),
	// the type switch doesn't match and templates inside aren't resolved

	recipeCtx := newRecipeCtx(t, nil)
	parentCtx := newSequenceCtx(t, recipeCtx, "parent", map[string]interface{}{
		"prompt": "hello world",
	})

	t.Run("recipe.InputMap with templates inside", func(t *testing.T) {
		// Import recipe package to use InputMap type
		// This simulates the exact scenario: map[string]interface{} with "form" => recipe.InputMap

		// Create an InputMap (custom type) with a template inside
		inputMap := map[string]interface{}{
			"question": "${{ inputs.prompt }}",
		}

		// Try to resolve it
		resolved, err := parentCtx.resolveValue(inputMap)
		require.NoError(t, err)

		t.Logf("Resolved InputMap: %+v", resolved)

		// Check if it was resolved
		resolvedMap, ok := resolved.(map[string]interface{})
		if !ok {
			// Might still be recipe.InputMap type
			if inputMapType, ok := resolved.(map[string]interface{}); ok {
				resolvedMap = map[string]interface{}(inputMapType)
			}
		}
		require.NotNil(t, resolvedMap, "should resolve to a map type")

		question := resolvedMap["question"]
		questionStr, ok := question.(string)
		require.True(t, ok, "question should be a string")

		// This is the bug - the template is NOT resolved
		assert.Equal(t, "hello world", questionStr, "template in InputMap should be resolved")
		assert.NotContains(t, questionStr, "${{", "template markers should be gone")
	})

	t.Run("nested structure with recipe.InputMap", func(t *testing.T) {
		// This is the exact scenario from the user's report
		rawInputs := map[string]interface{}{
			"form": map[string]interface{}{
				"question": "${{ inputs.prompt }}",
			},
		}

		// This is what compiler does
		resolved, err := parentCtx.ResolveMap(rawInputs)
		require.NoError(t, err)

		t.Logf("Resolved inputs: %+v", resolved)

		// Navigate to the nested value
		form, ok := resolved["form"]
		require.True(t, ok, "form should exist")

		// The form might be recipe.InputMap or map[string]interface{}
		var formMap map[string]interface{}
		if m, ok := form.(map[string]interface{}); ok {
			formMap = m
		} else if im, ok := form.(map[string]interface{}); ok {
			formMap = map[string]interface{}(im)
		} else {
			t.Fatalf("form is unexpected type: %T", form)
		}

		question := formMap["question"]
		questionStr, ok := question.(string)
		require.True(t, ok, "question should be a string")

		// This assertion will FAIL due to the bug
		assert.Equal(t, "hello world", questionStr, "template in nested InputMap should be resolved")
		assert.NotContains(t, questionStr, "${{", "template markers should be gone from nested InputMap")
	})
}

func TestPureCELMode(t *testing.T) {
	recipeCtx := newRecipeCtx(t, nil)
	ctx := newStateMachineCtx(t, recipeCtx, "test", map[string]interface{}{
		"retry_count": 2,
		"max_retries": 3,
	})

	seqCtx := newSequenceCtx(t, ctx, "validate-seq", ctx.TemplateData.ContainerInputs)

	addOpOutput(t, seqCtx, "validate", map[string]interface{}{
		"valid": true,
	})

	tests := []struct {
		name      string
		expr      string
		expected  bool
		expectErr bool
	}{
		{
			name:     "simple boolean",
			expr:     "true",
			expected: true,
		},
		{
			name:     "CEL comparison",
			expr:     "inputs.retry_count < inputs.max_retries",
			expected: true,
		},
		{
			name:     "CEL logical AND",
			expr:     "sequence.validate.outputs.valid == true && inputs.retry_count < 3",
			expected: true,
		},
		{
			name:     "CEL logical OR",
			expr:     "inputs.retry_count > 5 || sequence.validate.outputs.valid == true",
			expected: true,
		},
		{
			name:      "invalid CEL",
			expr:      "invalid syntax ${{",
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

func TestGoTemplateInterpolation(t *testing.T) {
	recipeCtx := newRecipeCtx(t, nil)
	ctx := newSequenceCtx(t, recipeCtx, "go-template", map[string]interface{}{
		"name": "Alice",
		"age":  42,
	})
	addOpOutput(t, ctx, "step1", map[string]interface{}{
		"status":    "ok",
		"completed": true,
	})

	t.Run("root functions are available", func(t *testing.T) {
		result, err := ctx.resolveTemplate("user={{ inputs.name }} status={{ sequence.step1.outputs.status }}")
		require.NoError(t, err)
		assert.Equal(t, "user=Alice status=ok", result)
	})

	t.Run("context root function supports CEL-style path", func(t *testing.T) {
		result, err := ctx.resolveTemplate("invoke_seq={{ context.invocation.sequence }}")
		require.NoError(t, err)
		assert.Contains(t, result, "invoke_seq=")
	})

	t.Run("single simple go template returns bool scalar", func(t *testing.T) {
		result, err := ctx.resolveTemplate("{{ sequence.step1.outputs.completed }}")
		require.NoError(t, err)
		assert.Equal(t, true, result)
	})

	t.Run("single simple go template returns number scalar", func(t *testing.T) {
		result, err := ctx.resolveTemplate("{{ inputs.age }}")
		require.NoError(t, err)
		assert.Equal(t, 42, result)
	})

	t.Run("single complex go template remains string for compatibility", func(t *testing.T) {
		result, err := ctx.resolveTemplate("{{ sequence.step1.outputs }}")
		require.NoError(t, err)
		assert.IsType(t, "", result)
		assert.Contains(t, result.(string), "status:ok")
	})

	t.Run("CEL and Go templates can coexist at top level", func(t *testing.T) {
		result, err := ctx.resolveTemplate("go={{ inputs.name }} cel=${{ inputs.name }}")
		require.NoError(t, err)
		assert.Equal(t, "go=Alice cel=Alice", result)
	})
}

func TestGoTemplate_FunctionRegistryBuiltinsAndCustom(t *testing.T) {
	builder := funcregistry.NewBuilder().WithDefaults()
	funcregistry.AddZeroFuncWithContext(builder, "cells", func(ctx context.Context, taskCtx contextual.TaskExecutionContext) ([]funcregistry.CELCell, error) {
		return []funcregistry.CELCell{
			{Name: "alpha", ID: "1", Path: "/alpha"},
			{Name: "beta", ID: "2", Path: "/beta"},
		}, nil
	})

	opts := DefaultResolutionOptions()
	opts.CELOptionsProvider = builder
	ctx, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, map[string]interface{}{
		"payload": map[string]interface{}{
			"user": map[string]interface{}{"name": "Ada"},
		},
	}, contextual.JobContext{}, opts)
	require.NoError(t, err)

	result, err := ctx.resolveTemplate("name={{ jq inputs.payload \".user.name\" }} cell={{ (index (cells) 0).name }}")
	require.NoError(t, err)
	assert.Equal(t, "name=Ada cell=alpha", result)

	jsonResult, err := ctx.resolveTemplate("cells_json={{ cells | to_json }}")
	require.NoError(t, err)
	assert.Contains(t, jsonResult, "cells_json=[")
	assert.Contains(t, jsonResult, "\"name\":\"alpha\"")
}

func TestTemplateContextExposesHostAndOpEnvironmentPaths(t *testing.T) {
	ctx, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{
		Environment: contextual.EnvironmentContext{
			WorkdirPath:    contextual.WorkdirPathSentinel,
			WorktreePath:   contextual.WorktreePathSentinel,
			ArtifactInbox:  contextual.ArtifactInboxSentinel,
			ArtifactOutbox: contextual.ArtifactOutboxSentinel,
			Host: contextual.EnvironmentPathContext{
				Workdir:      contextual.WorkdirPathSentinel,
				WorktreePath: contextual.WorktreePathSentinel,
				Inbox:        contextual.ArtifactInboxSentinel,
				Outbox:       contextual.ArtifactOutboxSentinel,
			},
			Op: contextual.EnvironmentPathContext{
				Workdir:      contextual.OpWorkdirPathSentinel,
				WorktreePath: contextual.OpWorktreePathSentinel,
				Inbox:        contextual.OpArtifactInboxSentinel,
				Outbox:       contextual.OpArtifactOutboxSentinel,
			},
		},
	})
	require.NoError(t, err)

	hostInbox, err := ctx.resolveTemplate("{{ context.environment.host.inbox }}")
	require.NoError(t, err)
	require.Equal(t, contextual.ArtifactInboxSentinel, hostInbox)

	opInbox, err := ctx.resolveTemplate("{{ context.environment.op.inbox }}")
	require.NoError(t, err)
	require.Equal(t, contextual.OpArtifactInboxSentinel, opInbox)

	opWorktree, err := ctx.resolveTemplate("${{ context.environment.op.worktree_path }}")
	require.NoError(t, err)
	require.Equal(t, contextual.OpWorktreePathSentinel, opWorktree)
}
