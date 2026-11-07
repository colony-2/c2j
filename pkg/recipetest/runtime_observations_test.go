package recipetest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/internal/celltest"
	coreops "github.com/colony-2/c2j/pkg/ops"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestObserveWorktreeThroughSymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	require.NoError(t, os.Mkdir(root, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "candidate"), []byte("retained"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "large"), []byte(strings.Repeat("x", 65537)), 0644))
	alias := filepath.Join(base, "alias")
	require.NoError(t, os.Symlink(root, alias))
	got, err := observeWorktree(alias, []string{"candidate", "absent", "large"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"candidate": "retained"}, got)
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("not in worktree"), 0644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
	_, err = observeWorktree(alias, []string{"escape"})
	require.ErrorContains(t, err, "escapes worktree")
	_, err = observeWorktree(alias, []string{"../outside"})
	require.ErrorContains(t, err, "remain within")
}

const observationRecipe = `id: bridge
sequence:
- id: develop
  state:
    initial: verify
    states:
      verify:
        sequence:
        - id: adaptive-verify
          sequence:
          - id: helper
            op: observation_probe
          outputs:
            ok: false
            allowed: false
            result: '${{ sequence.helper.outputs }}'
            nothing: null
            empty: {}
            artifact_refs:
              diagnostic.txt: {kind: value, format: text, value: diagnostic-content}
            agent_artifacts: {}
        outputs:
          ok: '${{ sequence["adaptive-verify"].outputs.ok }}'
          allowed: '${{ sequence["adaptive-verify"].outputs.allowed }}'
          result: '${{ sequence["adaptive-verify"].outputs.result }}'
          artifact_refs: '${{ sequence["adaptive-verify"].outputs.artifact_refs }}'
          agent_artifacts: '${{ sequence["adaptive-verify"].outputs.agent_artifacts }}'
        transitions:
        - to: dispatch
          when: true
          payload:
            failed: '${{ !states.verify.outputs.ok }}'
            result: '${{ states.verify.outputs.result }}'
      dispatch:
        op: observation_dispatch
        inputs:
          candidate: '${{ transition.payload.result.candidate }}'
        transitions:
        - {to: verify, when: true}
`

func observationSelection() *ObservationConfig {
	return &ObservationConfig{
		Scopes: []ScopeObservationSelector{
			{NodePath: "bridge/develop/verify/sequence/adaptive-verify", Outputs: []string{"ok", "allowed", "result", "artifact_refs", "agent_artifacts", "nothing", "empty", "missing"}, RetainArtifacts: []string{"diagnostic.txt"}},
			{NodePath: "bridge/develop/verify", Outputs: []string{"ok", "allowed", "result", "artifact_refs", "agent_artifacts"}},
		},
		Transitions: []TransitionObservationSelector{{MachinePath: "bridge/develop", From: "verify", To: "dispatch"}},
		Entries:     []EntryObservationSelector{{MachinePath: "bridge/develop", State: "dispatch"}},
	}
}

func TestNativeAcceptedObservationsSurviveRootFailure(t *testing.T) {
	probes, dispatches := 0, 0
	probe := coreops.NewActivityMappedOpV2[map[string]any, map[string]any](coreops.OpMetadata{Type: "observation_probe"}, func(_ coreops.OpDependencies, _ context.Context, _ map[string]any) (map[string]any, error) {
		probes++
		return map[string]any{"candidate": fmt.Sprintf("candidate-%d", probes), "passed": false}, nil
	})
	dispatch := coreops.NewActivityMappedOpV2[struct {
		Candidate string `json:"candidate"`
	}, map[string]any](coreops.OpMetadata{Type: "observation_dispatch"}, func(_ coreops.OpDependencies, _ context.Context, _ struct {
		Candidate string `json:"candidate"`
	}) (map[string]any, error) {
		dispatches++
		if dispatches == 2 {
			return nil, fmt.Errorf("expected iteration cap")
		}
		return map[string]any{}, nil
	})
	withRegisteredCoreOps(t, probe, dispatch)
	result := RunCase(celltest.Context(context.Background()), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "observations", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: observationRecipe}, Case{ID: "bounded", Type: "integration_case", Runtime: &RuntimeCase{Observe: observationSelection(), ExpectError: "expected iteration cap"}, Assertions: []Assertion{{Type: "cel_true", Expr: `observation_collection.complete && observations.filter(o, o.kind == "state_entered").size() == 2`}}}, ExecutionOptions{ArtifactMode: "inline"})
	require.Equal(t, "passed", result.Status, result.FailureReason)
	require.Equal(t, 2, probes)
	require.Equal(t, 2, dispatches)
	require.NotNil(t, result.Runtime.ObservationCollection)
	require.True(t, result.Runtime.ObservationCollection.Complete, "%+v", result.Runtime)
	records := result.Runtime.Observations
	require.Len(t, records, 8, "%+v", records)
	ids := map[string]bool{}
	for _, r := range records {
		require.Equal(t, "bounded", r["case_id"])
		require.NotEmpty(t, r["job_id"])
		require.Equal(t, "root", r["cell"])
		require.NotEmpty(t, r["scope_id"])
		require.NotEmpty(t, r["machine_scope_id"])
		require.Equal(t, "test_local_scope_callback", r["identity_kind"])
		id := r["id"].(string)
		require.False(t, ids[id])
		ids[id] = true
	}
	first := records[0]
	require.Equal(t, records[1]["scope_id"], first["state_scope_id"])
	second := records[4]
	require.NotEqual(t, first["scope_id"], second["scope_id"])
	out := first["outputs"].(map[string]any)
	require.Equal(t, false, out["ok"])
	require.Equal(t, false, out["allowed"])
	require.Contains(t, out, "nothing")
	require.Nil(t, out["nothing"])
	require.Equal(t, map[string]any{}, out["empty"])
	require.NotContains(t, out, "missing")
	require.Equal(t, false, first["output_presence"].(map[string]any)["missing"])
	require.Equal(t, "candidate-1", out["result"].(map[string]any)["candidate"])
	require.Equal(t, "candidate-2", second["outputs"].(map[string]any)["result"].(map[string]any)["candidate"])
	for _, i := range []int{2, 6} {
		selection, entry := records[i], records[i+1]
		require.Equal(t, "transition_selected", selection["kind"])
		require.Equal(t, "state_entered", entry["kind"])
		require.Equal(t, selection["id"], entry["transition_id"])
		require.Equal(t, records[i-1]["scope_id"], selection["source_scope_id"])
		require.Equal(t, selection["payload"], entry["incoming_transition"].(map[string]any)["payload"])
	}
	require.Len(t, result.Runtime.ObservationArtifacts, 2)
	for _, m := range result.Runtime.ObservationArtifacts {
		require.Equal(t, "retained", m.Status)
		artifact := result.Artifacts[strings.TrimPrefix(m.Path, "artifacts/")]
		bytes, err := base64.StdEncoding.DecodeString(artifact.ContentBase64)
		require.NoError(t, err)
		require.Equal(t, "diagnostic-content", string(bytes))
		require.False(t, artifact.Truncated)
	}
}

func TestNativeObservationsSeparateMachinesAndChildren(t *testing.T) {
	withRegisteredCoreOps(t, append(recipeops.GetOps(), commandop.GetOp())...)
	machine := `state:
    initial: verify
    states:
      verify:
        op: command_execution
        inputs: {run: fixture}
        transitions:
        - {to: dispatch, when: true, payload: {actual: value}}
      dispatch:
        op: command_execution
        inputs: {run: '${{ transition.payload.actual }}'}
`
	child := "id: child\n" + strings.ReplaceAll(machine, "\n    ", "\n  ")
	// Two nested machines share authored state names; the child adds another job.
	parent := "id: parent\nsequence:\n- id: first\n  " + machine + "- id: child-run\n  op: recipe.run_and_get_result\n  inputs: {name: child, cell_name: root, git_ref: main}\n- id: second\n  " + machine
	observe := &ObservationConfig{}
	for _, path := range []string{"parent/first", "parent/second", "child"} {
		observe.Transitions = append(observe.Transitions, TransitionObservationSelector{MachinePath: path, From: "verify", To: "dispatch"})
		observe.Entries = append(observe.Entries, EntryObservationSelector{MachinePath: path, State: "dispatch"})
	}
	result := RunCase(celltest.Context(context.Background()), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "observations", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: parent}, Case{ID: "identity", Type: "integration_case", Runtime: &RuntimeCase{Observe: observe, Cells: map[string]CellFixture{"root": {Files: map[string]string{".c2/recipes/child.yaml": child}}}}, Mocks: Mocks{Ops: []OpMock{{Repeat: true, Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"exit_code": 0}}}}}}, ExecutionOptions{})
	require.Equal(t, "passed", result.Status, result.FailureReason)
	require.True(t, result.Runtime.ObservationCollection.Complete)
	require.Len(t, result.Runtime.Observations, 6)
	jobs := map[any]bool{}
	for i := 0; i < 6; i += 2 {
		selection, entry := result.Runtime.Observations[i], result.Runtime.Observations[i+1]
		jobs[selection["job_id"]] = true
		require.Equal(t, selection["id"], entry["transition_id"])
		require.Equal(t, selection["job_id"], entry["job_id"])
		require.Equal(t, selection["machine_scope_id"], entry["machine_scope_id"])
		require.NotEmpty(t, selection["source_scope_id"])
	}
	require.Len(t, jobs, 2)
}

func TestNativeStoredDiagnosticAndObserverOff(t *testing.T) {
	withRegisteredCoreOps(t, commandop.GetOp())
	source := `id: stored
sequence:
- id: verify
  sequence:
  - id: helper
    op: command_execution
    inputs: {run: fixture}
  outputs:
    ok: false
    artifact_refs:
      diagnostic.txt: '${{ sequence.helper.artifacts["diagnostic.txt"] }}'
- id: fail
  op: command_execution
  inputs: {run: fixture}
`
	run := func(observe *ObservationConfig) CaseRunResult {
		return RunCase(celltest.Context(context.Background()), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "observations", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: source}, Case{ID: "stored", Type: "integration_case", Runtime: &RuntimeCase{Observe: observe, ExpectError: "deliberate root failure", Secrets: map[string]string{"token": "private-value"}}, Mocks: Mocks{Ops: []OpMock{
			{Match: OpMockMatch{NodePath: "stored/verify/helper"}, Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"exit_code": 0}, Artifacts: map[string]string{"diagnostic.txt": "stored diagnostic private-value"}}},
			{Match: OpMockMatch{NodePath: "stored/fail"}, Behavior: MockBehavior{Mode: "fail", Error: &TestError{Message: "deliberate root failure"}}},
		}}}, ExecutionOptions{})
	}
	off := run(nil)
	require.Equal(t, "passed", off.Status, off.FailureReason)
	require.Nil(t, off.Runtime.ObservationCollection)
	raw, err := json.Marshal(off.Runtime)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "observations")
	on := run(&ObservationConfig{Scopes: []ScopeObservationSelector{{NodePath: "stored/verify", Outputs: []string{"ok", "artifact_refs"}, RetainArtifacts: []string{"diagnostic.txt"}}}})
	require.Equal(t, off.Status, on.Status, on.FailureReason)
	require.True(t, on.Runtime.ObservationCollection.Complete, "%+v", on.Runtime.ObservationCollection)
	require.Len(t, on.Runtime.ObservationArtifacts, 1)
	m := on.Runtime.ObservationArtifacts[0]
	require.Equal(t, "retained", m.Status)
	require.Equal(t, "stored", m.Ref.(map[string]any)["kind"])
	bytes, err := base64.StdEncoding.DecodeString(on.Artifacts[strings.TrimPrefix(m.Path, "artifacts/")].ContentBase64)
	require.NoError(t, err)
	require.Equal(t, "stored diagnostic [REDACTED]", string(bytes))
	require.Len(t, on.Runtime.Calls, len(off.Runtime.Calls))
	for i := range on.Runtime.Calls {
		require.Equal(t, off.Runtime.Calls[i].Outputs, on.Runtime.Calls[i].Outputs)
		offInputs, onInputs := off.Runtime.Calls[i].Inputs, on.Runtime.Calls[i].Inputs
		require.True(t, strings.HasSuffix(offInputs["working_directory"].(string), "/worktree"))
		require.True(t, strings.HasSuffix(onInputs["working_directory"].(string), "/worktree"))
		delete(offInputs, "working_directory")
		delete(onInputs, "working_directory")
		require.Equal(t, offInputs, onInputs)
	}
}

func TestNativeObservationRetryAndCatchRoute(t *testing.T) {
	withRegisteredCoreOps(t, commandop.GetOp())
	t.Run("retry accepts only final callback", func(t *testing.T) {
		source := `id: retried
sequence:
- id: verify
  retry: {maximum_attempts: 2, initial_interval: 1ms}
  sequence:
  - id: helper
    op: command_execution
    inputs: {run: fixture}
  outputs: {ok: false}
`
		result := RunCase(celltest.Context(context.Background()), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "observations", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: source}, Case{ID: "retry", Type: "integration_case", Runtime: &RuntimeCase{Observe: &ObservationConfig{Scopes: []ScopeObservationSelector{{NodePath: "retried/verify", Outputs: []string{"ok"}}}}}, Mocks: Mocks{Ops: []OpMock{
			{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "fail", Error: &TestError{Message: "retry me"}}},
			{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"exit_code": 0}}},
		}}}, ExecutionOptions{})
		require.Equal(t, "passed", result.Status, result.FailureReason)
		require.True(t, result.Runtime.ObservationCollection.Complete)
		require.Len(t, result.Runtime.Observations, 1)
		r := result.Runtime.Observations[0]
		require.Equal(t, float64(2), r["callback_attempt"])
		require.Equal(t, float64(1), r["scope_visit"])
		require.Equal(t, false, r["outputs"].(map[string]any)["ok"])
	})
	t.Run("route is not accepted ordinary source", func(t *testing.T) {
		source := `id: routed
state:
  initial: verify
  states:
    verify:
      op: command_execution
      inputs: {run: fixture}
      catch:
      - id: handle
        when: true
        to: dispatch
        payload: {ordinary: false}
      transitions:
      - {to: dispatch, when: true}
    dispatch:
      op: command_execution
      inputs: {run: fixture}
`
		result := RunCase(celltest.Context(context.Background()), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "observations", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: source}, Case{ID: "route", Type: "integration_case", Runtime: &RuntimeCase{Observe: &ObservationConfig{Transitions: []TransitionObservationSelector{{MachinePath: "routed", From: "verify", To: "dispatch"}}, Entries: []EntryObservationSelector{{MachinePath: "routed", State: "dispatch"}}}}, Mocks: Mocks{Ops: []OpMock{
			{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "fail", Error: &TestError{Message: "route me"}}},
			{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"exit_code": 0}}},
		}}}, ExecutionOptions{})
		require.Equal(t, "passed", result.Status, result.FailureReason)
		require.True(t, result.Runtime.ObservationCollection.Complete)
		require.Len(t, result.Runtime.Observations, 2)
		selection, entry := result.Runtime.Observations[0], result.Runtime.Observations[1]
		require.Equal(t, "route_selected", selection["kind"])
		require.Equal(t, "", selection["source_scope_id"])
		require.NotNil(t, selection["failure"])
		require.Equal(t, selection["id"], entry["transition_id"])
		require.Empty(t, entry["source_scope_id"])
		passed, _ := evaluateAssertion(`observations.filter(o, o.kind == "transition_selected").size() == 1`, nil, nil, "passed", result.Runtime)
		require.False(t, passed)
	})
}

func TestNativeIncompleteObservationFailsAssertionsWithoutChangingExecution(t *testing.T) {
	withRegisteredCoreOps(t, commandop.GetOp())
	source := `id: capture-error
sequence:
- id: verify
  sequence: []
  outputs: {ok: false}
outputs: {done: true}
`
	cfg := &ObservationConfig{Scopes: []ScopeObservationSelector{{NodePath: "capture-error/verify", Outputs: []string{"ok"}}}, Limits: ObservationLimits{Bytes: 1}}
	result := RunCase(celltest.Context(context.Background()), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "observations", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: source}, Case{ID: "capture-error", Type: "integration_case", Runtime: &RuntimeCase{Observe: cfg}, Assertions: []Assertion{{Type: "cel_true", Expr: `observation_collection.complete && observations.size() == 1`}}}, ExecutionOptions{})
	require.Equal(t, "failed", result.Status)
	require.Equal(t, "assertion_failure", result.FailureCategory)
	require.Equal(t, true, result.Outputs["done"])
	require.False(t, result.Runtime.ObservationCollection.Complete)
	require.Equal(t, 1, result.Runtime.ObservationCollection.Reasons["byte_limit"])
}
