package recipetest

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCELAssertionsEvaluateObservedCalls(t *testing.T) {
	report := &RuntimeReport{Calls: []OpCall{{NodePath: "build/design", Op: "agent", Inputs: map[string]any{"prompt": "approved design"}}}}
	for _, example := range []struct {
		expr string
		pass bool
	}{
		{`outputs.merged == false && calls.size() == 1 && calls[0].inputs.prompt.contains("approved")`, true},
		{`calls.exists(c, c.op == "merge")`, false},
		{`outputs.merged`, false},
		{`calls[99].op == "merge"`, false},
		{`unknown`, false},
		{``, false},
	} {
		passed, _ := evaluateAssertion(example.expr, map[string]any{"merged": false}, nil, "passed", report)
		require.Equal(t, example.pass, passed, example.expr)
	}
}

func TestRuntimeFixturesRejectUnscopedEffectsAndUnsafePaths(t *testing.T) {
	for _, c := range []Case{
		{Mocks: Mocks{Ops: []OpMock{{Behavior: MockBehavior{Effects: &FixtureEffects{}}}}}},
		{Runtime: &RuntimeCase{Cells: map[string]CellFixture{"../elsewhere": {}}}},
		{Runtime: &RuntimeCase{Cells: map[string]CellFixture{"root": {Files: map[string]string{"../escape": "bad"}}}}},
		{Runtime: &RuntimeCase{Responses: []InputFixture{{Fields: map[string]any{"decision": "yes"}}}}},
	} {
		require.NotEmpty(t, validateRuntimeCase(HarnessOptions{}, c))
	}
}

func TestExpectedErrorRequiresMatchingExecutionFailure(t *testing.T) {
	for _, tc := range []struct{ status, reason, expected, want string }{
		{"failed", "schema rejected input", "rejected", "passed"},
		{"passed", "", "rejected", "failed"},
		{"failed", "unrelated error", "rejected", "failed"},
		{"timed_out", "rejected", "rejected", "failed"},
	} {
		result := CaseRunResult{Status: tc.status, FailureReason: tc.reason}
		applyExpectedError(&result, tc.expected)
		if result.Status != tc.want {
			t.Fatalf("%+v: %+v", tc, result)
		}
	}
}
