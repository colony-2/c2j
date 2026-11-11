package recipetest

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	inputop "github.com/colony-2/c2j/pkg/input"
	coreops "github.com/colony-2/c2j/pkg/ops"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/stretchr/testify/require"
)

func TestOrderingValidation(t *testing.T) {
	for _, gate := range []*AfterCall{{}, {NodePath: "x"}, {NodePath: "x", Occurrence: -1}} {
		require.NotEmpty(t, validateRuntimeCase(HarnessOptions{}, Case{Runtime: &RuntimeCase{Responses: []InputFixture{{NodePath: "input", AfterCall: gate}}}}))
	}
	require.Empty(t, validateRuntimeCase(HarnessOptions{}, Case{Runtime: &RuntimeCase{Responses: []InputFixture{{NodePath: "input", AfterCall: &AfterCall{NodePath: "x", Occurrence: 1}}}}}))
	for _, mode := range []string{"forever", "completed"} {
		require.NotEmpty(t, validateRuntimeCase(HarnessOptions{}, Case{Runtime: &RuntimeCase{Cells: map[string]CellFixture{"child": {}}}, Mocks: Mocks{Ops: []OpMock{{Behavior: MockBehavior{Effects: &FixtureEffects{Children: []ChildFixture{{Cell: "child", Await: mode}}}}}}}}))
	}
}

func TestOrderingEntryIsolation(t *testing.T) {
	f := &runtimeFixture{rootJobID: "parent", gateCounts: map[int]int{}, c: Case{Runtime: &RuntimeCase{Responses: []InputFixture{{AfterCall: &AfterCall{NodePath: "parent/go", Cell: "root", Occurrence: 2}}}}}}
	f.recordEntry("child-entry", "child", "root", "parent/go")
	f.recordEntry("other-cell", "parent", "other", "parent/go")
	f.recordEntry("other-path", "parent", "root", "parent/other")
	require.Zero(t, f.gateCounts[0])
	f.recordEntry("first", "parent", "root", "parent/go")
	f.recordEntry("first", "parent", "root", "parent/go")
	require.Equal(t, 1, f.gateCounts[0])
	f.recordEntry("second", "parent", "root", "parent/go")
	require.Equal(t, "gate_eligible", f.report.Events[len(f.report.Events)-1].Kind)
}

const orderingParent = `id: parent
sequence:
- id: submit
  op: command_execution
  inputs: {run: unused}
outputs:
  report: ${{ sequence.submit.outputs.report }}
  jobs: ${{ sequence.submit.jobs }}
`

const orderingChild = `id: child
sequence:
- id: release
  op: input
  inputs:
    form:
      request: {title: Release}
      response_schema: {type: object}
outputs: {ok: true}
`

func runOrdering(t *testing.T, child string, await string, responses []InputFixture, parent string, timeout string, expected ...string) CaseRunResult {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "child.yaml"), []byte(child), 0600))
	return RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: root}, "ordering", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: parent}, Case{
		ID: "ordering", Type: "integration_case", Runtime: &RuntimeCase{ExpectError: strings.Join(expected, ""), Cells: map[string]CellFixture{"root": {}, "child": {}}, Responses: responses},
		Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{NodePath: "parent/submit"}, Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"report": "configured"}, Effects: &FixtureEffects{Children: []ChildFixture{{Recipe: "child.yaml", Cell: "child", Await: await}}}}}}},
	}, ExecutionOptions{Timeout: timeout})
}

func TestOrderingTerminalChildren(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "failed"}[failed], func(t *testing.T) {
			child := orderingChild
			if failed {
				child = "id: child\nop: command_execution\ninputs: {run: no-fixture}\n"
			}
			var responses []InputFixture
			if !failed {
				responses = []InputFixture{{NodePath: "child/release", Response: map[string]any{}}}
			}
			result := runOrdering(t, child, "terminal", responses, orderingParent, "10s")
			require.Equal(t, "passed", result.Status, "%+v", result)
			require.Equal(t, "configured", result.Outputs["report"])
			var submitted, terminal, returned OrderingEvent
			for _, e := range result.Runtime.Events {
				switch e.Kind {
				case "child_submitted":
					submitted = e
				case "child_terminal":
					terminal = e
				case "fixture_return":
					if e.NodePath == "parent/submit" {
						returned = e
					}
				}
			}
			require.NotEmpty(t, submitted.JobID)
			require.Equal(t, submitted.JobID, terminal.JobID)
			require.Equal(t, map[bool]string{false: "completed", true: "failed"}[failed], terminal.Status)
			require.Less(t, submitted.Position, terminal.Position)
			require.Less(t, terminal.Position, returned.Position)
			require.NotEmpty(t, result.Outputs["jobs"])
			t.Logf("native terminal outcome=%s jobs=%v events=%+v", terminal.Status, result.Outputs["jobs"], result.Runtime.Events)
		})
	}
}

func TestOrderingGateBeforeResponse(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	result := runOrdering(t, orderingChild, "terminal", []InputFixture{{NodePath: "child/release", Response: map[string]any{}, AfterCall: &AfterCall{NodePath: "parent/submit", Occurrence: 1}}}, orderingParent, "10s")
	require.Equal(t, "passed", result.Status, "%+v", result)
	var eligible, release int
	for _, e := range result.Runtime.Events {
		if e.Kind == "gate_eligible" {
			eligible = e.Position
		}
		if e.Kind == "response_release" {
			release = e.Position
		}
	}
	require.Positive(t, eligible)
	require.Less(t, eligible, release)
}

func TestOrderingCancellation(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	result := runOrdering(t, orderingChild, "terminal", []InputFixture{{NodePath: "child/release", Response: map[string]any{}, AfterCall: &AfterCall{NodePath: "parent/unreachable", Occurrence: 1}}}, orderingParent, "1s", "deadline")
	require.NotEqual(t, "passed", result.Status)
	require.Contains(t, result.FailureReason, "parent/unreachable")
	require.Contains(t, result.FailureReason, "unfinished child")
}

func TestOrderingDeferredChildAcrossParentCalls(t *testing.T) {
	type probeInput struct {
		Job string `json:"job"`
	}
	probe := coreops.NewActivityMappedOpV2[probeInput, map[string]any](coreops.OpMetadata{Type: "ordering_probe"}, func(deps coreops.OpDependencies, ctx context.Context, in probeInput) (map[string]any, error) {
		input, err := inputop.NewRuntime(deps.WorkflowControl(), nil)
		if err != nil {
			return nil, err
		}
		key := deps.JobTool().GetJobKey()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			pending, err := input.ListPendingInputs(ctx, key.TenantId)
			if err != nil {
				return nil, err
			}
			for _, item := range pending {
				if item.Id == in.Job {
					status, err := deps.WorkflowControl().(workflowctl.JobInspector).InspectJob(ctx, jobdb.JobKey{TenantId: key.TenantId, JobId: in.Job})
					if err != nil {
						return nil, err
					}
					return map[string]any{"job": in.Job, "terminal": status.Terminal, "status": status.Status, "pending": true}, nil
				}
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ticker.C:
			}
		}
	})
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp(), probe), commandop.GetOp())...)
	parent := strings.Replace(orderingParent, "outputs:\n  report:", `- id: first
  op: ordering_probe
  inputs: {job: '${{ sequence.submit.jobs.job_ids[0] }}'}
- id: second
  op: ordering_probe
  inputs: {job: '${{ sequence.submit.jobs.job_ids[0] }}'}
- id: go
  op: input
  inputs:
    form:
      request: {title: Parent}
      response_schema: {type: object}
outputs:
  first: ${{ sequence.first.outputs }}
  second: ${{ sequence.second.outputs }}
  report:`, 1)
	result := runOrdering(t, orderingChild, "", []InputFixture{
		{NodePath: "child/release", Response: map[string]any{}, AfterCall: &AfterCall{NodePath: "parent/go", Occurrence: 1}},
		{NodePath: "parent/go", Response: map[string]any{}},
	}, parent, "10s")
	require.Equal(t, "passed", result.Status, "%+v", result)
	for _, key := range []string{"first", "second"} {
		out := result.Outputs[key].(map[string]any)
		require.Equal(t, false, out["terminal"])
		require.Equal(t, true, out["pending"])
	}
	var child string
	var entry, eligible, release int
	for _, e := range result.Runtime.Events {
		if e.Kind == "child_submitted" {
			require.Empty(t, child)
			child = e.JobID
		}
		if e.Kind == "operation_entry" && e.NodePath == "parent/go" {
			entry = e.Position
		}
		if e.Kind == "gate_eligible" {
			eligible = e.Position
		}
		if e.Kind == "response_release" && e.NodePath == "child/release" {
			release = e.Position
			require.Equal(t, child, e.JobID)
		}
	}
	require.Positive(t, entry)
	require.Less(t, entry, eligible)
	require.Less(t, eligible, release)
	require.Equal(t, child, result.Outputs["first"].(map[string]any)["job"])
	require.Equal(t, child, result.Outputs["second"].(map[string]any)["job"])
	t.Logf("native nonterminal observations: first=%v second=%v child=%s", result.Outputs["first"], result.Outputs["second"], child)
}

func TestOrderingDeniedSubmission(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	parent := strings.Replace(orderingParent, "  inputs: {run: unused}", "  perms: {jobdb: {jobs: {submit: false}}}\n  inputs: {run: unused}", 1)
	result := runOrdering(t, orderingChild, "terminal", nil, parent, "5s")
	require.NotEqual(t, "passed", result.Status)
	require.Contains(t, result.FailureReason, "denied")
	for _, e := range result.Runtime.Events {
		require.NotEqual(t, "child_submitted", e.Kind)
	}
}

func TestOrderingTimeoutCannotSatisfyExpectedError(t *testing.T) {
	for _, expected := range []string{"deadline", "unfinished child"} {
		r := CaseRunResult{Status: "timed_out", FailureReason: "deadline; unfinished child"}
		applyExpectedError(&r, expected)
		require.NotEqual(t, "passed", r.Status)
	}
}

func TestOrderingMalformedControls(t *testing.T) {
	for _, raw := range []string{
		`{"node_path":"input","after_call":[]}`,
		`{"node_path":"input","after_call":null}`,
		`{"node_path":"input","after_call":{"node_path":"x","occurrence":1,"typo":true}}`,
		`{"node_path":"input","after_call":{"node_path":"x","occurrence":"1"}}`,
	} {
		var response InputFixture
		require.Error(t, json.Unmarshal([]byte(raw), &response))
	}
	for _, raw := range []string{`{"await":""}`, `{"await":null}`, `{"await":"forever"}`, `{"await":[]}`} {
		var child ChildFixture
		require.Error(t, json.Unmarshal([]byte(raw), &child))
	}
}

func TestOrderingSkipsDeferredResponse(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp())
	result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "ordering", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: orderingChild}, Case{ID: "skip", Type: "integration_case", Runtime: &RuntimeCase{Responses: []InputFixture{
		{NodePath: "child/release", Response: map[string]any{}, AfterCall: &AfterCall{NodePath: "unreachable", Occurrence: 1}},
		{NodePath: "child/release", Response: map[string]any{}},
	}}}, ExecutionOptions{Timeout: "5s"})
	require.Equal(t, "failed", result.Status)
	require.Equal(t, "response_unused", result.FailureCategory)
	require.Len(t, result.Runtime.Reviews, 1)
	var released bool
	for _, e := range result.Runtime.Events {
		if e.Kind == "response_release" {
			require.Equal(t, 1, *e.ResponseIndex)
			released = true
		}
	}
	require.True(t, released)
}

func TestOrderingUnreachableGateWithoutBarrier(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	parent := strings.Replace(orderingParent, "outputs:\n  report:", `- id: await
  op: recipe.await_result_soft
  inputs: {target: {goal: "${{ sequence.submit.jobs.goal_ids[0] }}"}}
outputs:
  report:`, 1)
	result := runOrdering(t, orderingChild, "", []InputFixture{{NodePath: "child/release", Response: map[string]any{}, AfterCall: &AfterCall{NodePath: "unreachable", Occurrence: 1}}}, parent, "1s", "deadline")
	require.NotEqual(t, "passed", result.Status)
	require.Contains(t, result.FailureReason, "unreachable")
}

func TestOrderingDeniedCorrectionKeepsOriginalChild(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "child.yaml"), []byte("id: child\nsequence: []\noutputs: {accepted: true}\n"), 0600))
	parent := strings.Replace(orderingParent, "outputs:\n  report:", `- id: correction
  op: command_execution
  perms: {jobdb: {jobs: {submit: false}}}
  inputs: {run: unused}
outputs:
  report:`, 1)
	effects := &FixtureEffects{Children: []ChildFixture{{Recipe: "child.yaml", Cell: "child"}}}
	result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: root}, "ordering", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: parent}, Case{
		ID: "denied-correction", Type: "integration_case", Runtime: &RuntimeCase{Cells: map[string]CellFixture{"root": {}, "child": {}}},
		Mocks: Mocks{Ops: []OpMock{
			{Match: OpMockMatch{NodePath: "parent/submit"}, Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"report": "original"}, Effects: effects}},
			{Match: OpMockMatch{NodePath: "parent/correction"}, Behavior: MockBehavior{Mode: "return", Effects: effects}},
		}},
	}, ExecutionOptions{Timeout: "5s"})
	require.Equal(t, "failed", result.Status)
	require.Contains(t, result.FailureReason, "denied")
	var submitted []OrderingEvent
	for _, e := range result.Runtime.Events {
		if e.Kind == "child_submitted" {
			submitted = append(submitted, e)
		}
	}
	require.Len(t, submitted, 1)
	require.Equal(t, "parent/submit", submitted[0].NodePath)
}
