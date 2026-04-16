package template

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTemplate(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  []Segment
		expectErr bool
	}{
		{
			name:  "plain text",
			input: "Hello World",
			expected: []Segment{
				TextSegment{Text: "Hello World", Pos: 0},
			},
		},
		{
			name:  "single expression",
			input: "${{ inputs.name }}",
			expected: []Segment{
				ExpressionSegment{Expression: "inputs.name", Pos: 4},
			},
		},
		{
			name:  "text and expression",
			input: "Hello ${{ inputs.name }}",
			expected: []Segment{
				TextSegment{Text: "Hello ", Pos: 0},
				ExpressionSegment{Expression: "inputs.name", Pos: 10},
			},
		},
		{
			name:  "multiple expressions",
			input: "Hello ${{ inputs.name }}, your ID is ${{ inputs.id }}",
			expected: []Segment{
				TextSegment{Text: "Hello ", Pos: 0},
				ExpressionSegment{Expression: "inputs.name", Pos: 10},
				TextSegment{Text: ", your ID is ", Pos: 24},
				ExpressionSegment{Expression: "inputs.id", Pos: 41},
			},
		},
		{
			name:  "expression with double quotes",
			input: `${{ "text with }} inside" }}`,
			expected: []Segment{
				ExpressionSegment{Expression: `"text with }} inside"`, Pos: 4},
			},
		},
		{
			name:  "expression with single quotes",
			input: `${{ 'text with }} inside' }}`,
			expected: []Segment{
				ExpressionSegment{Expression: `'text with }} inside'`, Pos: 4},
			},
		},
		{
			name:  "escaped double quotes",
			input: `${{ "escaped \"quote\"" }}`,
			expected: []Segment{
				ExpressionSegment{Expression: `"escaped \"quote\""`, Pos: 4},
			},
		},
		{
			name:  "CEL single quote escape",
			input: `${{ 'don''t' }}`,
			expected: []Segment{
				ExpressionSegment{Expression: `'don''t'`, Pos: 4},
			},
		},
		{
			name:  "mixed quotes in expression",
			input: `${{ inputs.type == 'active' || inputs.code == "ABC" }}`,
			expected: []Segment{
				ExpressionSegment{Expression: `inputs.type == 'active' || inputs.code == "ABC"`, Pos: 4},
			},
		},
		{
			name:  "backslash in double quotes",
			input: `${{ "C:\\Users\\file" }}`,
			expected: []Segment{
				ExpressionSegment{Expression: `"C:\\Users\\file"`, Pos: 4},
			},
		},
		{
			name:  "literal backslash in single quotes",
			input: `${{ 'C:\Users\file' }}`,
			expected: []Segment{
				ExpressionSegment{Expression: `'C:\Users\file'`, Pos: 4},
			},
		},
		{
			name:  "complex interpolation",
			input: `[${{ scope.timestamp }}] User ${{ inputs.user_id }} performed ${{ inputs.action }}`,
			expected: []Segment{
				TextSegment{Text: "[", Pos: 0},
				ExpressionSegment{Expression: "scope.timestamp", Pos: 5},
				TextSegment{Text: "] User ", Pos: 23},
				ExpressionSegment{Expression: "inputs.user_id", Pos: 34},
				TextSegment{Text: " performed ", Pos: 51},
				ExpressionSegment{Expression: "inputs.action", Pos: 66},
			},
		},
		{
			name:      "unclosed expression",
			input:     "${{ inputs.name",
			expectErr: true,
		},
		{
			name:      "unclosed string in expression",
			input:     `${{ "unclosed string }}`,
			expectErr: true,
		},
		{
			name:  "empty expression",
			input: "${{ }}",
			expected: []Segment{
				ExpressionSegment{Expression: "", Pos: 4},
			},
		},
		{
			name:  "whitespace in expression",
			input: "${{  inputs.name  }}",
			expected: []Segment{
				ExpressionSegment{Expression: "inputs.name", Pos: 5},
			},
		},
		{
			name:      "nested go template delimiters in CEL are rejected",
			input:     `${{ "value: ${{}}" }}`,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segments, err := parseTemplate(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, segments)
		})
	}
}

func TestFindExpressionEnd(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		start     int
		expected  int
		expectErr bool
	}{
		{
			name:     "simple expression",
			input:    "${{ inputs.name }}",
			start:    3,
			expected: 16,
		},
		{
			name:     "expression with double quotes",
			input:    `${{ "text with }} inside" }}`,
			start:    3,
			expected: 26,
		},
		{
			name:     "expression with single quotes",
			input:    `${{ 'text with }} inside' }}`,
			start:    3,
			expected: 26,
		},
		{
			name:     "escaped quotes",
			input:    `${{ "escaped \"}}\" quote" }}`,
			start:    3,
			expected: 27,
		},
		{
			name:     "CEL single quote escape",
			input:    `${{ 'don''t forget' }}`,
			start:    3,
			expected: 20,
		},
		{
			name:      "unclosed expression",
			input:     "${{ inputs.name",
			start:     3,
			expectErr: true,
		},
		{
			name:      "unclosed string",
			input:     `${{ "unclosed }}`,
			start:     3,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			end, err := findExpressionEnd(tt.input, tt.start)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, end)
		})
	}
}

func TestIsInterpolationTemplate(t *testing.T) {
	tests := []struct {
		name     string
		segments []Segment
		expected bool
	}{
		{
			name: "single expression",
			segments: []Segment{
				ExpressionSegment{Expression: "inputs.name", Pos: 0},
			},
			expected: false, // Backward compatible - single expression returns raw
		},
		{
			name: "single text",
			segments: []Segment{
				TextSegment{Text: "Hello World", Pos: 0},
			},
			expected: false, // Plain text
		},
		{
			name: "text and expression",
			segments: []Segment{
				TextSegment{Text: "Hello ", Pos: 0},
				ExpressionSegment{Expression: "inputs.name", Pos: 6},
			},
			expected: true, // Mixed content needs interpolation
		},
		{
			name: "multiple expressions",
			segments: []Segment{
				ExpressionSegment{Expression: "inputs.first", Pos: 0},
				ExpressionSegment{Expression: "inputs.last", Pos: 10},
			},
			expected: true, // Multiple expressions need interpolation
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isInterpolationTemplate(tt.segments)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertToString(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{
			name:     "string",
			input:    "hello",
			expected: "hello",
		},
		{
			name:     "integer",
			input:    42,
			expected: "42",
		},
		{
			name:     "int64",
			input:    int64(42),
			expected: "42",
		},
		{
			name:     "float64 integer",
			input:    float64(42),
			expected: "42",
		},
		{
			name:     "float64 decimal",
			input:    3.14159,
			expected: "3.14159",
		},
		{
			name:     "boolean true",
			input:    true,
			expected: "true",
		},
		{
			name:     "boolean false",
			input:    false,
			expected: "false",
		},
		{
			name:     "nil",
			input:    nil,
			expected: "",
		},
		{
			name:     "map",
			input:    map[string]interface{}{"key": "value"},
			expected: `{"key":"value"}`,
		},
		{
			name:     "slice",
			input:    []interface{}{"a", "b"},
			expected: `["a","b"]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertToString(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
