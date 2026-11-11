package recipetest

import (
	"context"
	"testing"

	inputop "github.com/colony-2/c2j/pkg/input"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestRuntimeNativeResponses(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp())
	for _, structured := range []bool{false, true} {
		name, form, rejection := "form", `kind: review
      title: Choose
      documents: {}
      fields:
      - id: action
        question: Continue or stop?
        type: multiple_choice
        required: true
        options:
        - {value: continue, label: Continue}
        - {value: stop, label: Stop}`, "fields.action: invalid multiple_choice answer"
		if structured {
			name, form, rejection = "structured", `request: {title: Choose}
      response_schema:
        type: object
        required: [action]
        properties:
          action: {type: string, enum: [continue, stop]}`, "response"
		}
		t.Run(name, func(t *testing.T) {
			target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `schema_version: 2
id: choice
sequence:
- id: choose
  op: input
  inputs:
    form:
      ` + form + `
outputs: {}
`}
			for _, tc := range []struct {
				name, expected, want, category string
				valid, later                   bool
				node, cell                     string
				attachment                     bool
			}{
				{name: "expected rejection", expected: rejection, want: "passed"},
				{name: "valid acceptance", valid: true, want: "passed"},
				{name: "unexpected rejection", want: "failed"},
				{name: "mismatched error", expected: "unrelated sentinel", want: "failed"},
				{name: "unmatched node", node: "choice/other", want: "failed", category: "response_unused"},
				{name: "unmatched cell", cell: "other", want: "failed", category: "response_unused"},
				{name: "later unattempted", expected: rejection, later: true, want: "failed", category: "response_unused"},
				{name: "attachment preparation", attachment: true, expected: "no such file or directory", want: "failed", category: "response_unused"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					action := "deploy"
					if tc.valid {
						action = "continue"
					}
					response := InputFixture{NodePath: "choice/choose", Cell: tc.cell}
					if tc.valid || tc.name == "expected rejection" {
						response.AfterCall = &AfterCall{NodePath: "choice/choose", Occurrence: 1}
					}
					if structured {
						response.Response = map[string]any{"action": action}
					} else {
						response.Fields = map[string]any{"action": action}
					}
					if tc.node != "" {
						response.NodePath = tc.node
					}
					if tc.attachment {
						response.Attachments = map[string]string{"file": "missing.txt"}
					}
					responses := []InputFixture{response}
					if tc.later {
						responses = append(responses, response)
					}
					result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: t.TempDir()}, "native-responses", target, Case{ID: tc.name, Type: "integration_case", Runtime: &RuntimeCase{ExpectError: tc.expected, Responses: responses}}, ExecutionOptions{})
					require.Equal(t, tc.want, result.Status, "%+v", result)
					require.Equal(t, tc.category, result.FailureCategory, result.FailureReason)
					require.NotNil(t, result.Runtime)
					if tc.valid {
						require.Len(t, result.Runtime.Reviews, 1)
					} else {
						require.Empty(t, result.Runtime.Reviews)
					}
					if tc.name == "unexpected rejection" {
						require.Contains(t, result.FailureReason, rejection)
					}
					if tc.name == "mismatched error" {
						require.Contains(t, result.FailureReason, "expected error containing")
						require.Contains(t, result.FailureReason, rejection)
					}
					if tc.later {
						require.Contains(t, result.FailureReason, "response 1")
					}
				})
			}
		})
	}
}
