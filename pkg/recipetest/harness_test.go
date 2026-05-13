package recipetest

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	recipecore "github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template/funcregistry"
)

func TestValidateCaseInlineRecipe(t *testing.T) {
	validation := ValidateCase(context.Background(), HarnessOptions{}, "p1", inlineInputTarget(), Case{
		ID:   "c1",
		Type: "recipe_case",
	})
	if !validation.Valid {
		t.Fatalf("expected valid case, got errors: %+v", validation.Errors)
	}
	if validation.CaseHash == "" {
		t.Fatal("expected case hash")
	}
}

func TestRunCaseAllowsMockedArtifactBindings(t *testing.T) {
	opts := HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build()}
	target := TargetRecipe{
		Mode:   "inline_recipe",
		Format: "yaml",
		Content: `
id: mocked-artifact-binding
version: "1.0.0"
input_schema: {}
sequence:
  - id: write
    op: command_execution
    inputs:
      run: "echo write"
  - id: read
    op: command_execution
    artifacts:
      foo.txt: '${{ sequence.write.artifacts["foo.txt"] }}'
    inputs:
      run: "cat foo.txt"
outputs:
  result: "{{ sequence.read.outputs.stdout }}"
`,
	}
	testCase := Case{
		ID:   "artifact-binding-mock",
		Type: "recipe_case",
		Mocks: Mocks{
			Ops: []OpMock{
				{
					Match: OpMockMatch{Op: "command_execution"},
					Behavior: MockBehavior{
						Mode:      "return",
						Outputs:   map[string]any{"stdout": "ok"},
						Artifacts: map[string]string{"foo.txt": "payload"},
					},
				},
				{
					Match: OpMockMatch{Op: "command_execution"},
					Behavior: MockBehavior{
						Mode:      "return",
						Outputs:   map[string]any{"stdout": "ok"},
						Artifacts: map[string]string{"foo.txt": "payload"},
					},
				},
			},
		},
	}

	validation := ValidateCase(context.Background(), opts, "recipe-test-project", target, testCase)
	if !validation.Valid {
		t.Fatalf("validation errors: %+v", validation.Errors)
	}

	resp := RunCase(context.Background(), opts, "recipe-test-project", target, testCase, ExecutionOptions{ArtifactMode: "inline"})
	if resp.Status != "passed" {
		t.Fatalf("status = %q, failure reason: %s", resp.Status, resp.FailureReason)
	}
	if resp.Outputs["result"] != "ok" {
		t.Fatalf("result = %#v, want ok", resp.Outputs["result"])
	}
	if _, ok := resp.Artifacts["foo.txt"]; !ok {
		t.Fatalf("expected inline artifact, got %#v", resp.Artifacts)
	}
}

func TestRunCaseBlocksUnmockedOpInIsolatedMode(t *testing.T) {
	resp := RunCase(context.Background(), HarnessOptions{}, "p1", TargetRecipe{
		Mode:    "inline_recipe",
		Format:  "yaml",
		Content: "version: '1.0'\nid: test\nop: test_unmocked_op\ninputs:\n  foo: bar\n",
	}, Case{
		ID:   "c2",
		Type: "recipe_case",
	}, ExecutionOptions{Mode: "isolated"})

	if resp.Status != "failed" {
		t.Fatalf("status = %q, want failed", resp.Status)
	}
	if resp.FailureCategory != "policy_blocked" {
		t.Fatalf("failure category = %q, want policy_blocked", resp.FailureCategory)
	}
}

func TestRunCaseMockedInputAndArtifactLimit(t *testing.T) {
	resp := RunCase(context.Background(), HarnessOptions{}, "p1", inlineInputTarget(), Case{
		ID:   "c3",
		Type: "recipe_case",
		Mocks: Mocks{
			Ops: []OpMock{
				{
					Match: OpMockMatch{Op: "input"},
					Behavior: MockBehavior{
						Mode:      "return",
						Outputs:   map[string]any{"response": "ok"},
						Artifacts: map[string]string{"report.txt": "1234567890"},
					},
				},
			},
		},
		Assertions: []Assertion{
			{Type: "output_equals", Path: "response", Value: "ok"},
			{Type: "artifact_exists", Path: "report.txt"},
		},
	}, ExecutionOptions{Mode: "isolated", ArtifactMode: "inline", ArtifactMaxBytes: 4})

	if resp.Status != "passed" {
		t.Fatalf("status = %q, failure reason: %s", resp.Status, resp.FailureReason)
	}
	if len(resp.Assertions) != 2 {
		t.Fatalf("expected 2 assertions, got %#v", resp.Assertions)
	}
	art, ok := resp.Artifacts["report.txt"]
	if !ok {
		t.Fatal("expected inline artifact")
	}
	if !art.Truncated {
		t.Fatal("expected truncated artifact")
	}
	decoded, err := base64.StdEncoding.DecodeString(art.ContentBase64)
	if err != nil {
		t.Fatalf("decode artifact: %v", err)
	}
	if string(decoded) != "1234" {
		t.Fatalf("artifact content = %q, want 1234", string(decoded))
	}
}

func TestRunCaseMockedChoiceInputDoesNotPanicBeforeMock(t *testing.T) {
	resp := RunCase(context.Background(), HarnessOptions{}, "p1", TargetRecipe{
		Mode:   "inline_recipe",
		Format: "yaml",
		Content: `
version: '1.0'
id: choice-input
op: input
inputs:
  form:
    question: Continue?
    type: multiple_choice
    options:
      - value: continue
        label: Continue
      - value: cancel
        label: Cancel
`,
	}, Case{
		ID:   "choice-input-mock",
		Type: "recipe_case",
		Mocks: Mocks{
			Ops: []OpMock{
				{
					Match: OpMockMatch{Op: "input"},
					Behavior: MockBehavior{
						Mode:    "return",
						Outputs: map[string]interface{}{"response": "cancel"},
					},
				},
			},
		},
		Assertions: []Assertion{
			{Type: "output_equals", Path: "response", Value: "cancel"},
		},
	}, ExecutionOptions{Mode: "isolated"})

	if resp.Status != "passed" {
		t.Fatalf("status = %q, failure reason: %s", resp.Status, resp.FailureReason)
	}
	if len(resp.Diagnostics.MockHits) != 1 {
		t.Fatalf("mock hits = %#v, want one input mock hit", resp.Diagnostics.MockHits)
	}
}

func TestValidateCaseRecipeSelectorUsesResolver(t *testing.T) {
	rec := mustLoadRecipe(t, inlineInputTarget().Content)
	resolver := &fakeTargetResolver{recipe: rec, hash: "resolved-hash"}

	validation := ValidateCase(context.Background(), HarnessOptions{Resolver: resolver}, "p1", TargetRecipe{
		Mode:     "recipe_selector",
		Selector: "default",
	}, Case{ID: "c4", Type: "recipe_case"})

	if !validation.Valid {
		t.Fatalf("validation errors: %+v", validation.Errors)
	}
	if resolver.target.Selector != "default" {
		t.Fatalf("resolver target = %#v", resolver.target)
	}
}

func TestRunCaseOpCaseScopeEnforced(t *testing.T) {
	resp := RunCase(context.Background(), HarnessOptions{}, "p1", inlineInputTarget(), Case{
		ID:     "c5",
		Type:   "op_case",
		Target: map[string]interface{}{"node_path": "non.existent.path"},
		Mocks: Mocks{
			Ops: []OpMock{
				{
					Match: OpMockMatch{Op: "input"},
					Behavior: MockBehavior{
						Mode:    "return",
						Outputs: map[string]interface{}{"response": "ok"},
					},
				},
			},
		},
	}, ExecutionOptions{Mode: "isolated"})

	if resp.Status != "failed" {
		t.Fatalf("status = %q, want failed", resp.Status)
	}
	if resp.FailureCategory != "policy_blocked" {
		t.Fatalf("failure category = %q, want policy_blocked", resp.FailureCategory)
	}
}

func TestValidateCasePassthroughDependencyRequirement(t *testing.T) {
	validation := ValidateCase(context.Background(), HarnessOptions{}, "p1", inlineInputTarget(), Case{
		ID:   "c6",
		Type: "recipe_case",
		Mocks: Mocks{
			Ops: []OpMock{
				{
					Match:    OpMockMatch{Op: "input"},
					Behavior: MockBehavior{Mode: "passthrough"},
				},
			},
		},
		Options: map[string]interface{}{
			"policy": map[string]interface{}{
				"required_dependencies": []interface{}{"database"},
			},
		},
	})

	if validation.Valid {
		t.Fatal("expected passthrough dependency validation to fail")
	}
	if len(validation.Errors) == 0 {
		t.Fatal("expected validation errors")
	}
}

func TestRunCaseCellsTemplateUsesInjectedProviderAndProjectID(t *testing.T) {
	const projectID = "proj-cells-template"

	builder := funcregistry.NewBuilder().WithDefaults()
	funcregistry.AddZeroFuncWithContext(builder, "cells", func(_ context.Context, taskCtx contextual.TaskExecutionContext) ([]funcregistry.CELCell, error) {
		if taskCtx.Workflow.ProjectId != projectID {
			return nil, fmt.Errorf("cells: expected project_id %q, got %q", projectID, taskCtx.Workflow.ProjectId)
		}
		return []funcregistry.CELCell{
			{Name: "alpha", ID: "1", Path: "/cells/alpha", Description: "alpha cell"},
		}, nil
	})

	resp := RunCase(context.Background(), HarnessOptions{CELOptions: builder}, projectID, TargetRecipe{
		Mode:    "inline_recipe",
		Format:  "yaml",
		Content: "version: '1.0'\nid: test\nop: input\ninputs:\n  form:\n    question: \"{{ cells | to_json }}\"\n    type: short_answer\n",
	}, Case{
		ID:   "c7",
		Type: "recipe_case",
		Mocks: Mocks{
			Ops: []OpMock{
				{
					Match: OpMockMatch{Op: "input"},
					Behavior: MockBehavior{
						Mode:    "return",
						Outputs: map[string]interface{}{"response": "ok"},
					},
				},
			},
		},
	}, ExecutionOptions{Mode: "isolated"})

	if resp.Status != "passed" {
		t.Fatalf("status = %q, failure reason: %s", resp.Status, resp.FailureReason)
	}
}

func TestSelectOpMockConsumesDuplicatesInDeclarationOrder(t *testing.T) {
	mocks := []OpMock{
		{
			Match: OpMockMatch{NodePath: "root.loop", Op: "input"},
			Behavior: MockBehavior{
				Mode: "return",
			},
		},
		{
			Match: OpMockMatch{NodePath: "root.loop", Op: "input"},
			Behavior: MockBehavior{
				Mode: "return",
			},
		},
	}

	consumed := map[int]struct{}{}

	idx, ok := selectOpMock(mocks, "root.loop", "input", consumed)
	if !ok || idx != 0 {
		t.Fatalf("first selection = %d/%v, want 0/true", idx, ok)
	}
	consumed[idx] = struct{}{}

	idx, ok = selectOpMock(mocks, "root.loop", "input", consumed)
	if !ok || idx != 1 {
		t.Fatalf("second selection = %d/%v, want 1/true", idx, ok)
	}
	consumed[idx] = struct{}{}

	if _, ok = selectOpMock(mocks, "root.loop", "input", consumed); ok {
		t.Fatal("expected duplicate mocks to be exhausted")
	}
}

func TestJobContextReusesMockWithinInvocationButNotAcrossInvocations(t *testing.T) {
	j := newTestJobContext(
		"recipe-test-project",
		Case{
			ID:   "reuse-by-invocation",
			Type: "recipe_case",
			Mocks: Mocks{
				Ops: []OpMock{
					{
						Match: OpMockMatch{NodePath: "root.loop", Op: "input"},
						Behavior: MockBehavior{
							Mode: "return",
						},
					},
				},
			},
		},
		TestPolicy{},
		coreops.NewServiceDepsBuilder().Build(),
	)

	_, ok := j.selectOpMockForInvocation("root.loop::input::1", "root.loop", "input")
	if !ok {
		t.Fatal("expected first invocation to match")
	}
	if len(j.consumedMockIdxs) != 1 {
		t.Fatalf("consumed mocks = %d, want 1", len(j.consumedMockIdxs))
	}

	_, ok = j.selectOpMockForInvocation("root.loop::input::1", "root.loop", "input")
	if !ok {
		t.Fatal("expected same invocation to reuse mock")
	}
	if len(j.consumedMockIdxs) != 1 {
		t.Fatalf("consumed mocks after reuse = %d, want 1", len(j.consumedMockIdxs))
	}

	_, ok = j.selectOpMockForInvocation("root.loop::input::2", "root.loop", "input")
	if ok {
		t.Fatal("expected second invocation to miss after mock was consumed")
	}
	if !hasOpMockCandidate(j.caseDef.Mocks.Ops, "root.loop", "input") {
		t.Fatal("expected exhausted mock candidate to still be detectable")
	}
}

type fakeTargetResolver struct {
	target TargetRecipe
	recipe *recipecore.Recipe
	hash   string
}

func (f *fakeTargetResolver) ResolveRecipeTarget(_ context.Context, _ string, target TargetRecipe) (*recipecore.Recipe, string, []Issue, []Issue) {
	f.target = target
	return f.recipe, f.hash, nil, nil
}

func inlineInputTarget() TargetRecipe {
	return TargetRecipe{
		Mode:    "inline_recipe",
		Format:  "yaml",
		Content: "version: '1.0'\nid: test\nop: input\ninputs:\n  form:\n    question: hi\n    type: short_answer\n",
	}
}

func mustLoadRecipe(t *testing.T, raw string) *recipecore.Recipe {
	t.Helper()
	rec, err := recipecore.LoadRecipeFromString([]byte(raw))
	if err != nil {
		t.Fatalf("load recipe: %v", err)
	}
	return rec
}
