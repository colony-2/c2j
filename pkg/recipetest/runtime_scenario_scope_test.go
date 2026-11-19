package recipetest

import (
	"testing"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/stretchr/testify/require"
)

func TestScenarioAmbiguousRecordedRestart(t *testing.T) {
	withRegisteredCoreOps(t, commandop.GetOp())
	c := Case{ID: "ambiguous-restart", Type: "integration_case", Runtime: &RuntimeCase{Backend: "sqlite", Controls: &RuntimeControls{Scenario: &Scenario{
		Jobs:     map[string]ScenarioJob{"root": {Root: true}},
		Events:   []ScenarioEvent{{ID: "failed", Kind: "settled", Job: "root", Status: "failed"}},
		Actions:  []ScenarioAction{{ID: "restart", AfterAll: []string{"failed"}, Do: "restart_job", Target: "root", Restart: &ScenarioRestart{Path: []string{"$job", "state_machine:repeat", "state:visit", "sequence:sequence", "op:done"}, RequestID: "ambiguous-request", Continuation: "continuation", RequiredPrefix: []TaskSelection{{TaskType: "command_execution:command_execution", NodePath: "repeat/visit/sequence/done", Attempt: 1, Occurrence: 1}}}}},
		Outcomes: map[string]ScenarioOutcome{"root": {Status: "failed", Message: "selected failure"}, "continuation": {Status: "completed"}},
	}}}, Mocks: Mocks{Ops: []OpMock{
		{Match: OpMockMatch{NodePath: "repeat/visit/sequence/done"}, Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"again": true}}},
		{Match: OpMockMatch{NodePath: "repeat/visit/sequence/done"}, Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"again": false}}},
		{Match: OpMockMatch{NodePath: "repeat/fail/command_execution"}, Behavior: MockBehavior{Mode: "fail", Error: &TestError{Message: "selected failure"}}},
	}}}
	target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `id: repeat
state:
 initial: visit
 states:
  visit:
   sequence:
   - id: done
     op: command_execution
     inputs: {run: unused}
   outputs: {again: '${{ sequence.done.outputs.again }}'}
   transitions:
   - to: visit
     when: states.visit.outputs.again
   - to: fail
  fail:
   op: command_execution
   inputs: {run: unused}
`}
	r := runScenario(t, c, target, HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()})
	require.Equal(t, "failed", r.Status)
	require.Contains(t, r.FailureReason, "matches 2 recorded scopes")
	require.NotContains(t, r.Runtime.Controls.Scenario.Jobs, "continuation")
}
