package recipetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const includeMockChild = `id: child
version: "1"
input_schema:
  package: {type: string, required: true}
inputs: {package: '${{ inputs.package }}'}
sequence:
  - id: real
    op: command_execution
    inputs: {run: "printf actual"}
outputs:
  original: '${{ inputs.package }}'
`

const includeMockState = `id: parent
version: "1"
state:
  initial: check_package
  states:
    check_package:
      include: ./child.yaml
      inputs: {package: sample}
      expect_outputs:
        type: object
        required: [ready]
        properties: {ready: {type: boolean}}
      transitions:
        - to: publish_package
          when: states.check_package.outputs.ready
        - to: check_package
    publish_package:
      include: ./child.yaml
      inputs: {package: sample}
      artifacts:
        check.txt: '${{ states.check_package.artifacts["check.txt"] }}'
      transitions:
        - to: actual
    actual:
      include: ./child.yaml
      inputs: {package: unmocked}
outputs:
  version: '${{ states.publish_package.outputs.version }}'
  actual: '${{ states.actual.outputs.original }}'
`

func expandIncludeMockTarget(t *testing.T, parent string) TargetRecipe {
	t.Helper()
	root := t.TempDir()
	output, err := exec.Command("git", "-C", root, "init", "--quiet").CombinedOutput()
	require.NoError(t, err, "%s", output)
	rootFile := filepath.Join(root, "parent.yaml")
	require.NoError(t, os.WriteFile(rootFile, []byte(parent), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "child.yaml"), []byte(includeMockChild), 0600))
	target, _, err := ExpandInlineRecipeTarget(t.Context(), TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: parent},
		InlineTargetExpansionOptions{RootFile: rootFile, WorkingDir: root})
	require.NoError(t, err)
	return target
}

func TestNativeIncludeMocks(t *testing.T) {
	target := expandIncludeMockTarget(t, includeMockState)
	c := Case{
		ID: "include-mocks", Type: "integration_case", Runtime: &RuntimeCase{},
		Mocks: Mocks{Ops: []OpMock{{
			Match:    OpMockMatch{Op: "command_execution", NodePath: "parent/actual/actual/child/real"},
			Behavior: MockBehavior{Mode: "passthrough"},
		}}, Includes: map[string][]IncludeMockResponse{
			"root/check_package": {
				{Mode: "return", Outputs: map[string]any{"ready": false}},
				{Mode: "return", Outputs: map[string]any{"ready": true}, Artifacts: map[string]string{"check.txt": "checked"}},
			},
			"root/publish_package": {
				{Mode: "return", Outputs: map[string]any{"version": "1.2.0"}},
			},
		}},
		Assertions: []Assertion{{Type: "cel_true", Expr: `
size(include_calls) == 3 &&
include_calls[0].node_path == "root/check_package" &&
include_calls[1].node_path == "root/check_package" &&
include_calls[2].node_path == "root/publish_package" &&
include_calls[2].inputs.package == "sample" &&
"check.txt" in include_calls[2].artifact_refs`}},
	}
	result := RunCase(t.Context(), HarnessOptions{WorkRoot: t.TempDir()}, "native-include", target, c, ExecutionOptions{Timeout: "25s"})
	require.Equal(t, "passed", result.Status, "%s: %s %#v", result.FailureCategory, result.FailureReason, result.Assertions)
	require.Equal(t, "1.2.0", result.Outputs["version"])
	require.Equal(t, "unmocked", result.Outputs["actual"])
	require.Len(t, result.Runtime.Calls, 1, "only the unmocked child body runs")
	require.Equal(t, "real", filepath.Base(result.Runtime.Calls[0].NodePath))
	c.Runtime = nil
	validation := ValidateCase(t.Context(), HarnessOptions{}, "native-include", target, c)
	require.False(t, validation.Valid)
	require.Contains(t, validation.Errors[0].Message, "native runtime")
}

func TestNativeIncludeMockResponseAccounting(t *testing.T) {
	const parent = `id: parent
version: "1"
sequence:
  - id: call
    include: ./child.yaml
    inputs: {package: sample}
    retry: {maximum_attempts: 2, initial_interval: 1ms}
    expect_outputs:
      type: object
      required: [ready]
      properties: {ready: {type: boolean}}
  - id: skipped
    include: ./child.yaml
    when: false
    inputs: {package: skipped}
outputs:
  ready: '${{ sequence.call.outputs.ready }}'
`
	target := expandIncludeMockTarget(t, parent)
	fail := IncludeMockResponse{Mode: "fail", Error: &TestError{Code: "unavailable", Message: "try again"}}
	pass := IncludeMockResponse{Mode: "return", Outputs: map[string]any{"ready": true}}
	for _, tc := range []struct {
		name, path       string
		responses        []IncludeMockResponse
		skipped          []IncludeMockResponse
		status, reason   string
		expectedAttempts int
	}{
		{name: "retry and optional skipped", path: "root/[0]", responses: []IncludeMockResponse{fail, pass}, skipped: []IncludeMockResponse{{Mode: "return", Optional: true}}, status: "passed", expectedAttempts: 2},
		{name: "unknown", path: "root/[2]", responses: []IncludeMockResponse{pass}, status: "invalid", reason: "unknown include call site"},
		{name: "extra", path: "root/[0]", responses: []IncludeMockResponse{fail}, status: "failed", reason: "unexpected call 2", expectedAttempts: 2},
		{name: "unused", path: "root/[0]", responses: []IncludeMockResponse{pass, pass}, status: "failed", reason: "response 2 was not used", expectedAttempts: 1},
		{name: "output contract", path: "root/[0]", responses: []IncludeMockResponse{{Mode: "return", Outputs: map[string]any{"ready": 17}}, pass}, status: "failed", reason: "output contract", expectedAttempts: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mocks := map[string][]IncludeMockResponse{tc.path: tc.responses}
			if tc.skipped != nil {
				mocks["root/[1]"] = tc.skipped
			}
			c := Case{ID: tc.name, Type: "integration_case", Runtime: &RuntimeCase{}, Mocks: Mocks{Includes: mocks}}
			result := RunCase(t.Context(), HarnessOptions{WorkRoot: t.TempDir()}, "native-include", target, c, ExecutionOptions{Timeout: "20s"})
			require.Equal(t, tc.status, result.Status, "%s: %s", result.FailureCategory, result.FailureReason)
			require.Contains(t, result.FailureReason, tc.reason)
			if tc.expectedAttempts > 0 {
				require.NotNil(t, result.Runtime)
				require.Len(t, result.Runtime.IncludeCalls, tc.expectedAttempts)
				require.Empty(t, result.Runtime.Calls)
			}
		})
	}
}

func TestNativeIncludeMockFailureCanRouteCatch(t *testing.T) {
	const parent = `id: parent
version: "1"
state:
  initial: call
  states:
    call:
      include: ./child.yaml
      inputs: {package: sample}
      catch:
        - when: failure_has_code(failure, "unavailable")
          to: recover
          payload: {message: fallback}
    recover:
      sequence: []
      outputs: {message: '${{ transition.payload.message }}'}
outputs: {message: '${{ states.recover.outputs.message }}'}
`
	target := expandIncludeMockTarget(t, parent)
	c := Case{
		ID: "catch", Type: "integration_case", Runtime: &RuntimeCase{},
		Mocks: Mocks{Includes: map[string][]IncludeMockResponse{
			"root/call": {{Mode: "fail", Error: &TestError{Code: "unavailable", Message: "native fixture error"}}},
		}},
	}
	result := RunCase(t.Context(), HarnessOptions{}, "native-include", target, c, ExecutionOptions{Timeout: "20s"})
	require.Equal(t, "passed", result.Status, result.FailureReason)
	require.Equal(t, "fallback", result.Outputs["message"])
	require.Len(t, result.Runtime.IncludeCalls, 1)
	require.Empty(t, result.Runtime.Calls)
}
