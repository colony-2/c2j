package recipetest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/colony-2/c2j/pkg/hookpolicy"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/stretchr/testify/require"
)

func TestScenarioHookPolicyAcrossReopen(t *testing.T) {
	probe := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "scenario_secret_probe"}, func(_ coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
		values, err := permissions.FromContext(ctx).Resolve(ctx, []string{"TEST_TOKEN"})
		if err != nil {
			return nil, err
		}
		if values.Secrets["TEST_TOKEN"].Value != "synthetic-reopen-private-token" {
			return nil, fmt.Errorf("synthetic secret was not resolved")
		}
		return map[string]any{"ok": true}, nil
	})
	withRegisteredCoreOps(t, commandop.GetOp(), probe)
	const hook = ".c2/recipes/hooks/probe.yaml"
	for _, allowed := range []bool{true, false} {
		name := "denied"
		if allowed {
			name = "allowed"
		}
		t.Run(name, func(t *testing.T) {
			entry := hookpolicy.Empty()
			if allowed {
				entry = hookpolicy.Entry{Perms: permissions.Spec{JobDB: &permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"TEST_TOKEN"}}}}, Trust: hookpolicy.SourceTrust{Secrets: map[string]hookpolicy.Trust{"TEST_TOKEN": {Mode: "candidate"}}}}
			}
			policyRaw, err := json.Marshal(hookpolicy.Document{Version: 1, Hooks: map[string]hookpolicy.Entry{hook: entry}})
			require.NoError(t, err)
			var policy any
			require.NoError(t, json.Unmarshal(policyRaw, &policy))
			c := Case{ID: name, Type: "integration_case", Runtime: &RuntimeCase{Backend: "sqlite", Secrets: map[string]string{"TEST_TOKEN": "synthetic-reopen-private-token"}, Config: map[string]any{hookpolicy.TenantKey: policy}, Cells: map[string]CellFixture{"root": {Files: map[string]string{hook: "id: probe\nsequence:\n- id: resolve\n  op: scenario_secret_probe\noutputs: {ok: '${{ sequence.resolve.outputs.ok }}'}\n"}}}, Controls: &RuntimeControls{Scenario: &Scenario{
				Jobs:          map[string]ScenarioJob{"root": {Root: true}},
				DispatchHolds: []DispatchHold{{ID: "ready", Job: "root", Task: TaskSelection{TaskType: "command_execution:command_execution", NodePath: "policy/ready", Attempt: 1, Occurrence: 1}}},
				Events:        []ScenarioEvent{{ID: "ready", Kind: "dispatch", Hold: "ready"}},
				Actions:       []ScenarioAction{{ID: "stop", Do: "stop_worker", AfterAll: []string{"ready"}}, {ID: "reopen", Do: "reopen_runtime", AfterAll: []string{"stop"}}, {ID: "release", Do: "release", Target: "ready", AfterAll: []string{"reopen"}}, {ID: "start", Do: "start_worker", AfterAll: []string{"release"}}},
				Outcomes:      map[string]ScenarioOutcome{"root": {Status: "completed"}},
			}}}, Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{NodePath: "policy/ready"}, Behavior: MockBehavior{Mode: "return"}}, {Match: OpMockMatch{Op: "scenario_secret_probe"}, Repeat: true, Behavior: MockBehavior{Mode: "passthrough"}}}}}
			target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `id: policy
sequence:
- id: before
  include: cell://.c2/recipes/hooks/probe.yaml
  catch:
  - when: true
    continue: {outputs: {ok: false}}
- id: ready
  op: command_execution
  inputs: {run: unused}
- id: after
  include: cell://.c2/recipes/hooks/probe.yaml
  catch:
  - when: true
    continue: {outputs: {ok: false}}
outputs:
 before: '${{ sequence.before.outputs.ok }}'
 after: '${{ sequence.after.outputs.ok }}'
`}
			r := runScenario(t, c, target, HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()})
			require.Equal(t, "passed", r.Status, r.FailureReason)
			require.Equal(t, allowed, r.Outputs["before"])
			require.Equal(t, allowed, r.Outputs["after"])
			raw, err := json.Marshal(r)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "synthetic-reopen-private-token")
			require.Len(t, r.Runtime.Controls.Scenario.Sessions, 6)
		})
	}
}
