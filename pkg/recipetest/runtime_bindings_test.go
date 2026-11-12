package recipetest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	inputop "github.com/colony-2/c2j/pkg/input"
	coreops "github.com/colony-2/c2j/pkg/ops"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func expr(s string) *string { return &s }

func TestResponseExpressions(t *testing.T) {
	out, err := computedResponse(`{"job": children.a, "report": json_encode({"ids": [children.a, children.b], "quote": "\"\n"})}`, map[string]string{"a": "actual-a", "b": "actual-b"})
	require.NoError(t, err)
	require.Equal(t, "actual-a", out["job"])
	var report map[string]any
	require.NoError(t, json.Unmarshal([]byte(out["report"].(string)), &report))
	require.Equal(t, []any{"actual-a", "actual-b"}, report["ids"])
	for _, expression := range []string{`1`, `null`, `[1]`, `{"x": b"bytes"}`, `{"x": duration("1s")}`, `{1: "bad"}`, `{"x": 0.0/0.0}`, `{"x": children.unready}`} {
		_, err := computedResponse(expression, map[string]string{})
		require.Error(t, err, expression)
	}
	for _, expression := range []string{`inputs.secret`, `secrets.key`, `environment.HOME`, `jobs[0]`, `children = {}`, `{`} {
		_, err := responseProgram(expression)
		require.Error(t, err, expression)
	}
}

func TestResponseValidation(t *testing.T) {
	for _, raw := range []string{`{"mode":"return","outputs_expr":null}`, `{"mode":"return","outputs_expr":""}`, `{"mode":"return","outputs":null,"outputs_expr":"{}"}`} {
		var m MockBehavior
		require.Error(t, json.Unmarshal([]byte(raw), &m))
	}
	for _, raw := range []string{`{"bind":null}`, `{"bind":""}`, `{"bind":1}`} {
		var child ChildFixture
		require.Error(t, json.Unmarshal([]byte(raw), &child))
	}
	for _, m := range []MockBehavior{{Mode: "return", OutputsExpr: expr("{")}, {Mode: "fail", OutputsExpr: expr("{}")}, {Mode: "return", OutputsExpr: expr("{}"), Outputs: map[string]any{}}} {
		require.NotEmpty(t, validateResponses(Case{Runtime: &RuntimeCase{}, Mocks: Mocks{Ops: []OpMock{{Behavior: m}}}}))
	}
	require.NotEmpty(t, validateResponses(Case{Mocks: Mocks{Ops: []OpMock{{Behavior: MockBehavior{Mode: "return", OutputsExpr: expr("{}")}}}}}))
	duplicate := Case{Runtime: &RuntimeCase{}, Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{NodePath: "parent/submit"}, Behavior: MockBehavior{Mode: "return", Effects: &FixtureEffects{Children: []ChildFixture{{Bind: "same"}, {Bind: "same"}}}}}}}}
	issues := validateResponses(duplicate)
	require.NotEmpty(t, issues)
	require.Contains(t, issues[0].Message, `"same"`)
	require.Contains(t, issues[0].Message, "parent/submit")
}

const responseParent = `id: parent
version: "1"
sequence:
- id: submit
  op: command_execution
  inputs: {run: unused}
- id: later
  op: command_execution
  inputs: {run: unused}
outputs:
  same: ${{ sequence.submit.outputs }}
  later: ${{ sequence.later.outputs }}
  jobs: ${{ sequence.submit.jobs }}
`

func responseCase() Case {
	return Case{ID: "bindings", Type: "integration_case", Runtime: &RuntimeCase{Cells: map[string]CellFixture{"root": {}, "child": {}}}, Mocks: Mocks{Ops: []OpMock{
		{Match: OpMockMatch{NodePath: "parent/submit"}, Behavior: MockBehavior{Mode: "return", OutputsExpr: expr(`{"a": children.a, "b": children.b}`), Effects: &FixtureEffects{Children: []ChildFixture{{Bind: "a", Recipe: "good.yaml", Cell: "child", Await: "terminal"}, {Bind: "b", Recipe: "bad.yaml", Cell: "child", Await: "terminal"}}}}},
		{Match: OpMockMatch{NodePath: "parent/later"}, Behavior: MockBehavior{Mode: "return", OutputsExpr: expr(`{"selected": {"job_id": children.b}, "report": json_encode({"success": children.a, "failure": children.b})}`)}},
	}}}
}

func runResponseCase(t *testing.T, c Case, parents ...string) CaseRunResult {
	parent := responseParent
	if len(parents) > 0 {
		parent = parents[0]
	}
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "good.yaml"), []byte("id: good\nop: command_execution\ninputs: {run: unused}\noutputs: {ok: true}\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "bad.yaml"), []byte("id: bad\nop: command_execution\ninputs: {run: unused}\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "part.txt"), []byte("published part"), 0600))
	c.Mocks.Ops = append(c.Mocks.Ops, OpMock{Match: OpMockMatch{NodePath: "good"}, Behavior: MockBehavior{Mode: "return", Artifacts: map[string]string{"success.txt": "actual artifact"}}}, OpMock{Match: OpMockMatch{NodePath: "bad"}, Behavior: MockBehavior{Mode: "fail", Error: &TestError{Message: "actual child failure"}}})
	return RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: root}, "bindings", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: parent}, c, ExecutionOptions{Timeout: "10s"})
}

func TestResponseNativeBindings(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	result := runResponseCase(t, responseCase())
	require.Equal(t, "passed", result.Status, "%+v", result)
	same := result.Outputs["same"].(map[string]any)
	var ids []string
	for _, event := range result.Runtime.Events {
		if event.Kind == "child_submitted" {
			ids = append(ids, event.JobID)
		}
	}
	require.Len(t, ids, 2)
	require.Equal(t, ids[0], same["a"])
	require.Equal(t, ids[1], same["b"])
	data, err := json.Marshal(result.Outputs["jobs"])
	require.NoError(t, err)
	var jobs struct {
		JobIDs []string `json:"job_ids"`
	}
	require.NoError(t, json.Unmarshal(data, &jobs))
	require.ElementsMatch(t, ids, jobs.JobIDs)
	later := result.Outputs["later"].(map[string]any)
	require.Equal(t, ids[1], later["selected"].(map[string]any)["job_id"])
	var report map[string]any
	require.NoError(t, json.Unmarshal([]byte(later["report"].(string)), &report))
	require.Equal(t, same["a"], report["success"])
	require.Equal(t, same["b"], report["failure"])
	t.Logf("native ids=%v jobs=%s", ids, data)
}

func TestResponseParallelCases(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	var wg sync.WaitGroup
	results := make([]CaseRunResult, 3)
	for i := range results {
		wg.Add(1)
		go func() { defer wg.Done(); results[i] = runResponseCase(t, responseCase()) }()
	}
	wg.Wait()
	seen := map[any]bool{}
	for _, result := range results {
		require.Equal(t, "passed", result.Status, "%+v", result)
		id := result.Outputs["same"].(map[string]any)["a"]
		require.False(t, seen[id])
		seen[id] = true
	}
}

func TestResponseNativeErrors(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	for _, tc := range []struct{ expression, expected string }{
		{`{"x": children.future}`, "future"}, {`1`, "JSON object"}, {`{"x": b"bytes"}`, "unsupported JSON"},
	} {
		t.Run(tc.expected, func(t *testing.T) {
			c := responseCase()
			c.Mocks.Ops[0].Behavior.OutputsExpr = expr(tc.expression)
			c.Mocks.Ops = c.Mocks.Ops[:1]
			c.Runtime.ExpectError = tc.expected
			r := runResponseCase(t, c)
			require.Equal(t, "passed", r.Status, "%+v", r)
		})
	}
}

func TestResponseReceiptReconstruction(t *testing.T) {
	f := &runtimeFixture{root: t.TempDir()}
	calls := 0
	submit := func() (childReceipt, error) {
		calls++
		return childReceipt{JobID: "broker-id", TenantID: "native"}, nil
	}
	first, fresh, err := f.boundChild("invocation/0", "a", "root:parent/submit", submit)
	require.NoError(t, err)
	require.True(t, fresh)
	// Drop the entire in-memory fixture object; recover only from retained bytes.
	f = &runtimeFixture{root: f.root}
	second, fresh, err := f.boundChild("invocation/0", "a", "root:parent/submit", submit)
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, first, second)
	require.Equal(t, 1, calls)
	_, _, err = f.boundChild("other/0", "a", "root:parent/later", submit)
	require.ErrorContains(t, err, "parent/later")
	require.ErrorContains(t, err, "parent/submit")
	_, _, err = f.boundChild("failed/0", "bad", "root:parent/failed", func() (childReceipt, error) { return childReceipt{}, fmt.Errorf("transport failed") })
	require.Error(t, err)
	bindings, err := f.responseBindings()
	require.NoError(t, err)
	require.NotContains(t, bindings, "bad")
	_, _, err = f.boundChild("failed/0", "bad", "root:parent/failed", submit)
	require.ErrorContains(t, err, "unresolved")
	require.Equal(t, 1, calls)
}

func TestResponseNativeDiagnosticsAndArtifacts(t *testing.T) {
	type probeInput struct {
		A string `json:"a"`
		B string `json:"b"`
	}
	probe := coreops.NewActivityMappedOpV2[probeInput, map[string]any](coreops.OpMetadata{Type: "response_inspect"}, func(deps coreops.OpDependencies, ctx context.Context, in probeInput) (map[string]any, error) {
		out := map[string]any{}
		for _, name := range []string{"a", "b"} {
			key := jobdb.JobKey{TenantId: deps.JobTool().GetJobKey().TenantId, JobId: map[string]string{"a": in.A, "b": in.B}[name]}
			inspection, err := deps.WorkflowControl().(workflowctl.JobInspector).InspectJob(ctx, key)
			if err != nil {
				return nil, err
			}
			require.True(t, inspection.Terminal)
			require.Equal(t, key, inspection.JobKey)
			if name == "b" {
				require.Equal(t, "failed", inspection.Status)
				require.Contains(t, inspection.FailureMessage, "actual child failure")
				out["diagnostic"] = inspection.FailureMessage
			} else {
				require.Equal(t, "completed", inspection.Status)
				result, err := deps.WorkflowControl().JobResult(ctx, key)
				if err != nil {
					return nil, err
				}
				artifacts, err := result.GetArtifacts()
				if err != nil {
					return nil, err
				}
				for _, artifact := range artifacts {
					if artifact.Name() == "success.txt" {
						data, err := artifact.Bytes(ctx)
						if err != nil {
							return nil, err
						}
						out["artifact"] = string(data)
					}
				}
				require.Equal(t, "actual artifact", out["artifact"])
			}
			out[name] = key.JobId
		}
		return out, nil
	})
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp(), probe), commandop.GetOp())...)
	c := responseCase()
	c.Mocks.Ops = c.Mocks.Ops[:1]
	parent := strings.Replace(responseParent, "- id: later\n  op: command_execution\n  inputs: {run: unused}", "- id: later\n  op: response_inspect\n  inputs:\n    a: '${{ sequence.submit.outputs.a }}'\n    b: '${{ sequence.submit.outputs.b }}'", 1)
	r := runResponseCase(t, c, parent)
	require.Equal(t, "passed", r.Status, "%+v", r)
	require.Equal(t, "actual artifact", r.Outputs["later"].(map[string]any)["artifact"])
	require.Equal(t, r.Outputs["same"].(map[string]any)["b"], r.Outputs["later"].(map[string]any)["b"])
	t.Logf("retrieved child evidence: %v", r.Outputs["later"])
}

func TestResponseNativeReusedBinding(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	c := responseCase()
	c.Mocks.Ops = c.Mocks.Ops[:1]
	c.Mocks.Ops[0].Match = OpMockMatch{Op: "command_execution", Cell: "root"}
	c.Mocks.Ops[0].Repeat = true
	c.Runtime.ExpectError = `binding "a" reused`
	r := runResponseCase(t, c)
	require.Equal(t, "passed", r.Status, "%+v", r)
	data, _ := json.Marshal(r.Outputs)
	require.Contains(t, string(data), "parent/later")
	require.Contains(t, string(data), "parent/submit")
	submissions := 0
	for _, e := range r.Runtime.Events {
		if e.Kind == "child_submitted" {
			submissions++
		}
	}
	require.Equal(t, 2, submissions)
}

func TestResponseNativeObjects(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	for _, collision := range []bool{false, true} {
		t.Run(fmt.Sprint(collision), func(t *testing.T) {
			c := responseCase()
			name := "checkpoint"
			if collision {
				name = "a"
				c.Runtime.ExpectError = `collides with object effect key "a"`
				c.Mocks.Ops = c.Mocks.Ops[:1]
			}
			c.Mocks.Ops[0].Behavior.Effects.Objects = map[string]ObjectFixture{name: {Type: "neutral/v1", Files: map[string]string{"part": "part.txt"}}}
			r := runResponseCase(t, c)
			require.Equal(t, "passed", r.Status, "%+v", r)
			if !collision {
				require.NotNil(t, r.Outputs["same"].(map[string]any)["checkpoint"])
			}
		})
	}
}

// This operation runs inside the native worker with its real broker and deps.
// Fail response evaluation after submission, retry on the same fixture, then
// reconstruct from disk and replay. The parent metadata must contain one child.
func TestResponseNativeRetryReplay(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "retry-child.yaml"), []byte("id: retry_child\nop: command_execution\ninputs: {run: unused}\n"), 0600))
	receiptRoot := t.TempDir()
	probe := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "response_retry"}, func(deps coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
		c := Case{Runtime: &RuntimeCase{}, Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{Op: "response_retry"}, Behavior: MockBehavior{Mode: "return", OutputsExpr: expr(`{"id": children.not_ready}`), Effects: &FixtureEffects{Children: []ChildFixture{{Bind: "retained", Recipe: "retry-child.yaml", Cell: "root"}}}}}}}}
		makeFixture := func() *runtimeFixture {
			return &runtimeFixture{root: receiptRoot, opts: HarnessOptions{FixtureRoot: root}, c: c, cells: map[string]string{"root": deps.GitContext().BaseRepo}, selected: map[string]int{}, callIndexes: map[string]int{}, used: map[int]struct{}{}}
		}
		f := makeFixture()
		_, err := f.invoke(deps, ctx, "response_retry", nil, map[string]any{})
		require.ErrorContains(t, err, "not_ready")
		receipts, err := f.receipts()
		require.NoError(t, err)
		require.Len(t, receipts, 1)
		id := receipts[0].JobID
		t.Logf("native submission receipt before response failure: %+v", receipts[0])
		require.NotEmpty(t, id)
		c.Mocks.Ops[0].Behavior.OutputsExpr = expr(`{"id": children.retained}`)
		f.c = c
		retry, err := f.invoke(deps, ctx, "response_retry", nil, map[string]any{})
		if err != nil {
			return nil, err
		}
		require.Equal(t, id, retry["id"])
		f = makeFixture()
		replay, err := f.invoke(deps, ctx, "response_retry", nil, map[string]any{})
		if err != nil {
			return nil, err
		}
		require.Equal(t, id, replay["id"])
		for _, event := range f.report.Events {
			require.NotEqual(t, "child_submitted", event.Kind, "replay must not emit another child submission")
		}
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			status, err := deps.WorkflowControl().(workflowctl.JobInspector).InspectJob(ctx, jobdb.JobKey{TenantId: deps.JobTool().GetJobKey().TenantId, JobId: id})
			if err != nil {
				return nil, err
			}
			if status.Terminal {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ticker.C:
			}
		}
		return replay, nil
	})
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp(), probe), commandop.GetOp())...)
	parent := "id: retry_parent\nsequence:\n- id: run\n  op: response_retry\noutputs:\n  id: '${{ sequence.run.outputs.id }}'\n  jobs: '${{ sequence.run.jobs }}'\n"
	c := Case{ID: "retry", Type: "integration_case", Runtime: &RuntimeCase{Cells: map[string]CellFixture{"root": {}}}, Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{NodePath: "retry_child"}, Behavior: MockBehavior{Mode: "return"}}}}}
	r := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: root}, "retry", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: parent}, c, ExecutionOptions{Timeout: "10s"})
	require.Equal(t, "passed", r.Status, "%+v", r)
	data, err := json.Marshal(r.Outputs["jobs"])
	require.NoError(t, err)
	var jobs struct {
		JobIDs []string `json:"job_ids"`
	}
	require.NoError(t, json.Unmarshal(data, &jobs))
	require.Equal(t, []string{r.Outputs["id"].(string)}, jobs.JobIDs)
	t.Logf("retained retry/replay child metadata: %s", data)
}

func TestResponseLiteralCompatibility(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	c := responseCase()
	c.Mocks.Ops[0].Behavior.OutputsExpr = nil
	literal := map[string]any{"a": "children.a", "b": "${{ children.b }}", "report": "json_encode(children)"}
	c.Mocks.Ops[0].Behavior.Outputs = literal
	r := runResponseCase(t, c)
	require.Equal(t, "passed", r.Status, "%+v", r)
	for name, value := range literal {
		require.Equal(t, value, r.Outputs["same"].(map[string]any)[name])
	}
}

func TestResponseValidationPrecedesEffects(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	duplicate := responseCase()
	duplicate.Mocks.Ops[0].Behavior.Effects.Children[1].Bind = "a"
	invalid := runResponseCase(t, duplicate)
	require.NotEqual(t, "passed", invalid.Status)
	require.Nil(t, invalid.Runtime, "duplicate declaration must fail before effects")
	for _, expression := range []string{"{", "inputs.secret"} {
		c := responseCase()
		c.Mocks.Ops[0].Behavior.OutputsExpr = expr(expression)
		r := runResponseCase(t, c)
		require.NotEqual(t, "passed", r.Status)
		require.Nil(t, r.Runtime, "invalid expression must not start native runtime")
	}
}

func TestResponseForwardBinding(t *testing.T) {
	withRegisteredCoreOps(t, append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())...)
	c := responseCase()
	c.Mocks.Ops[1].Behavior.Effects = c.Mocks.Ops[0].Behavior.Effects
	c.Mocks.Ops[0].Behavior.Effects = nil
	r := runResponseCase(t, c)
	require.NotEqual(t, "passed", r.Status)
	data, _ := json.Marshal(r.Outputs)
	require.Contains(t, string(data), "no such key: a")
	require.Contains(t, string(data), "parent/submit")
	for _, event := range r.Runtime.Events {
		require.NotEqual(t, "child_submitted", event.Kind)
	}
}

func TestResponseActiveChild(t *testing.T) {
	type probeInput struct {
		Job  string `json:"job"`
		Wait bool   `json:"wait"`
	}
	probe := coreops.NewActivityMappedOpV2[probeInput, map[string]any](coreops.OpMetadata{Type: "response_active"}, func(deps coreops.OpDependencies, ctx context.Context, in probeInput) (map[string]any, error) {
		input, err := inputop.NewRuntime(deps.WorkflowControl(), nil)
		if err != nil {
			return nil, err
		}
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			if in.Wait {
				status, err := deps.WorkflowControl().(workflowctl.JobInspector).InspectJob(ctx, jobdb.JobKey{TenantId: deps.JobTool().GetJobKey().TenantId, JobId: in.Job})
				if err != nil {
					return nil, err
				}
				if status.Terminal {
					return map[string]any{"job": in.Job, "terminal": true, "status": status.Status}, nil
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-ticker.C:
					continue
				}
			}
			pending, err := input.ListPendingInputs(ctx, deps.JobTool().GetJobKey().TenantId)
			if err != nil {
				return nil, err
			}
			for _, item := range pending {
				if item.Id == in.Job {
					status, err := deps.WorkflowControl().(workflowctl.JobInspector).InspectJob(ctx, jobdb.JobKey{TenantId: deps.JobTool().GetJobKey().TenantId, JobId: in.Job})
					if err != nil {
						return nil, err
					}
					return map[string]any{"job": in.Job, "terminal": status.Terminal, "pending": true}, nil
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
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "held.yaml"), []byte(orderingChild), 0600))
	parent := `id: parent
sequence:
- id: submit
  op: command_execution
  inputs: {run: unused}
- id: probe
  op: response_active
  inputs: {job: '${{ sequence.submit.outputs.id }}'}
- id: later
  op: command_execution
  inputs: {run: unused}
- id: go
  op: input
  inputs:
    form:
      request: {title: Continue}
      response_schema: {type: object}
- id: settled
  op: response_active
  inputs: {job: '${{ sequence.submit.outputs.id }}', wait: true}
outputs:
  settled: ${{ sequence.settled.outputs }}
  same: ${{ sequence.submit.outputs }}
  probe: ${{ sequence.probe.outputs }}
  later: ${{ sequence.later.outputs }}
  jobs: ${{ sequence.submit.jobs }}
`
	c := Case{ID: "active", Type: "integration_case", Runtime: &RuntimeCase{Cells: map[string]CellFixture{"root": {}, "child": {}}, Responses: []InputFixture{
		{NodePath: "child/release", Response: map[string]any{}, AfterCall: &AfterCall{NodePath: "parent/go", Occurrence: 1}},
		{NodePath: "parent/go", Response: map[string]any{}},
	}}, Mocks: Mocks{Ops: []OpMock{
		{Match: OpMockMatch{NodePath: "parent/submit"}, Behavior: MockBehavior{Mode: "return", OutputsExpr: expr(`{"id":children.held}`), Effects: &FixtureEffects{Children: []ChildFixture{{Bind: "held", Recipe: "held.yaml", Cell: "child"}}}}},
		{Match: OpMockMatch{NodePath: "parent/later"}, Behavior: MockBehavior{Mode: "return", OutputsExpr: expr(`{"id":children.held}`)}},
	}}}
	r := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: root}, "active", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: parent}, c, ExecutionOptions{Timeout: "10s"})
	require.Equal(t, "passed", r.Status, "%+v", r)
	probeOut := r.Outputs["probe"].(map[string]any)
	require.Equal(t, false, probeOut["terminal"])
	require.Equal(t, true, probeOut["pending"])
	require.Equal(t, "completed", r.Outputs["settled"].(map[string]any)["status"])
	id := r.Outputs["same"].(map[string]any)["id"]
	require.Equal(t, id, probeOut["job"])
	require.Equal(t, r.Outputs["same"], r.Outputs["later"])
	submissions, releases := 0, 0
	for _, e := range r.Runtime.Events {
		if e.Kind == "child_submitted" {
			submissions++
			require.Equal(t, id, e.JobID)
		}
		if e.Kind == "response_release" && e.NodePath == "child/release" {
			releases++
			require.Equal(t, id, e.JobID)
		}
	}
	require.Equal(t, 1, submissions)
	require.Equal(t, 1, releases)
	t.Logf("active child observed and released: %v", probeOut)
}
