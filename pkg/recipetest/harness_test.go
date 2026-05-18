package recipetest

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	recipecore "github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template/funcregistry"
	"github.com/colony-2/c2j/pkg/worker/commandop"
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

func TestRunCasePopulatesOpVisiblePathsWithoutSandbox(t *testing.T) {
	const opType = "test_op_visible_path_contract"
	type pathInput struct {
		RepoPath string `json:"repo_path" validate:"required"`
		HostPath string `json:"host_path" validate:"required"`
	}
	coreops.Register(coreops.NewActivityMappedOpV2[pathInput, map[string]interface{}](coreops.OpMetadata{Type: opType},
		func(_ coreops.OpDependencies, _ context.Context, input pathInput) (map[string]interface{}, error) {
			return map[string]interface{}{
				"repo_path": input.RepoPath,
				"host_path": input.HostPath,
			}, nil
		}))

	target := TargetRecipe{
		Mode:   "inline_recipe",
		Format: "yaml",
		Content: `
id: op-visible-paths
version: "1.0.0"
input_schema: {}
sequence:
  - id: check
    op: test_op_visible_path_contract
    inputs:
      repo_path: "{{ context.environment.op.worktree_path }}"
      host_path: "{{ context.environment.host.worktree_path }}"
outputs:
  repo_path: "{{ sequence.check.outputs.repo_path }}"
  host_path: "{{ sequence.check.outputs.host_path }}"
`,
	}
	testCase := Case{
		ID:   "op-visible-paths",
		Type: "recipe_case",
		Mocks: Mocks{
			Ops: []OpMock{{
				Match:    OpMockMatch{Op: opType},
				Behavior: MockBehavior{Mode: "passthrough"},
			}},
		},
	}

	resp := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build()}, "recipe-test-project", target, testCase, ExecutionOptions{})
	if resp.Status != "passed" {
		t.Fatalf("status = %q, failure reason: %s", resp.Status, resp.FailureReason)
	}
	repoPath, _ := resp.Outputs["repo_path"].(string)
	hostPath, _ := resp.Outputs["host_path"].(string)
	if repoPath == "" {
		t.Fatal("expected op-visible repo path to be populated")
	}
	if repoPath != hostPath {
		t.Fatalf("repo_path = %q, host_path = %q; want equal in no-sandbox test run", repoPath, hostPath)
	}
}

func TestRunCasePassthroughCommandUsesOpPathContractAndCollectsOutbox(t *testing.T) {
	withRegisteredCoreOps(t, commandop.GetOp())

	target := TargetRecipe{
		Mode:   "inline_recipe",
		Format: "yaml",
		Content: `
id: passthrough-paths
version: "1.0.0"
input_schema: {}
sequence:
  - id: seed
    op: command_execution
    inputs:
      run: |
        workdir="${{ context.environment.op.workdir }}"
        worktree="${{ context.environment.op.worktree_path }}"
        outbox="${{ context.environment.op.outbox }}"
        test -d "$workdir"
        test -d "$worktree"
        test -d "${{ context.environment.op.inbox }}"
        test -d "$outbox"
        case "$worktree" in "$workdir"/*) ;; *) echo "worktree escapes workdir" >&2; exit 17;; esac
        mkdir -p "$outbox/results"
        printf '%s' '{"ok":true}' > "$outbox/results/status.json"
  - id: read
    op: command_execution
    artifacts:
      results/status.json: '${{ sequence.seed.artifacts["results/status.json"] }}'
    inputs:
      run: cat "${{ context.environment.op.inbox }}/results/status.json"
outputs:
  status: "{{ sequence.read.outputs.stdout }}"
`,
	}
	testCase := Case{
		ID:   "passthrough-paths",
		Type: "recipe_case",
		Mocks: Mocks{Ops: []OpMock{
			{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "passthrough"}},
			{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "passthrough"}},
		}},
	}

	resp := RunCase(context.Background(), HarnessOptions{
		Deps:     coreops.NewServiceDepsBuilder().Build(),
		WorkRoot: t.TempDir(),
	}, "recipe-test-project", target, testCase, ExecutionOptions{ArtifactMode: "inline"})
	if resp.Status != "passed" {
		t.Fatalf("status = %q, failure reason: %s", resp.Status, resp.FailureReason)
	}
	if resp.Outputs["status"] != `{"ok":true}` {
		t.Fatalf("status output = %#v", resp.Outputs["status"])
	}
	if _, ok := resp.Artifacts["results/status.json"]; !ok {
		t.Fatalf("expected collected outbox artifact, got %#v", resp.Artifacts)
	}
}

func TestRunCaseTimeoutOverlaysRecipeTimeoutForPassthrough(t *testing.T) {
	const opType = "test_timeout_overlay_passthrough"
	withRegisteredCoreOps(t, coreops.NewActivityMappedOpV2[map[string]interface{}, map[string]interface{}](coreops.OpMetadata{Type: opType},
		func(_ coreops.OpDependencies, ctx context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			select {
			case <-ctx.Done():
				if cause := context.Cause(ctx); cause != nil {
					return map[string]interface{}{"done": false}, cause
				}
				return map[string]interface{}{"done": false}, ctx.Err()
			case <-time.After(2 * time.Second):
				return map[string]interface{}{"done": true}, nil
			}
		}))

	target := TargetRecipe{
		Mode:   "inline_recipe",
		Format: "yaml",
		Content: `
id: timeout-overlay
version: "1.0.0"
sequence:
  - id: slow
    op: test_timeout_overlay_passthrough
    inputs: {}
outputs:
  done: "{{ sequence.slow.outputs.done }}"
`,
	}
	testCase := Case{
		ID:   "timeout-overlay",
		Type: "recipe_case",
		Mocks: Mocks{Ops: []OpMock{{
			Match:    OpMockMatch{Op: opType},
			Behavior: MockBehavior{Mode: "passthrough"},
		}}},
		Assertions: []Assertion{{Type: "status_is", Status: "passed"}},
	}

	started := time.Now()
	resp := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build()}, "recipe-test-project", target, testCase, ExecutionOptions{Timeout: "75ms"})
	if resp.Status != "timed_out" {
		t.Fatalf("status = %q, failure category = %q, reason = %q", resp.Status, resp.FailureCategory, resp.FailureReason)
	}
	if resp.FailureCategory != "timeout" {
		t.Fatalf("failure category = %q, want timeout", resp.FailureCategory)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout overlay did not interrupt passthrough promptly; elapsed %s", elapsed)
	}
}

func TestWithRecipeTimeoutOverlayAppliesShorterTestTimeoutToRootRecipeTypes(t *testing.T) {
	existing := recipecore.Duration(2 * time.Second)
	overlay := 150 * time.Millisecond
	cases := []struct {
		name string
		rec  *recipecore.Recipe
	}{
		{
			name: "op",
			rec: &recipecore.Recipe{RecipeImpl: &recipecore.RecipeOp{
				RecipeMetadata: recipecore.RecipeMetadata{NodeMetadata: recipecore.NodeMetadata{ID: "root", Timeout: existing}},
			}},
		},
		{
			name: "sequence",
			rec: &recipecore.Recipe{RecipeImpl: &recipecore.RecipeSequence{
				RecipeMetadata: recipecore.RecipeMetadata{NodeMetadata: recipecore.NodeMetadata{ID: "root", Timeout: existing}},
			}},
		},
		{
			name: "state",
			rec: &recipecore.Recipe{RecipeImpl: &recipecore.RecipeState{
				RecipeMetadata: recipecore.RecipeMetadata{NodeMetadata: recipecore.NodeMetadata{ID: "root", Timeout: existing}},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withRecipeTimeoutOverlay(tc.rec, overlay)
			if timeout := recipeRootTimeout(t, got); time.Duration(timeout) != overlay {
				t.Fatalf("overlay timeout = %s, want %s", time.Duration(timeout), overlay)
			}
			if timeout := recipeRootTimeout(t, *tc.rec); timeout != existing {
				t.Fatalf("original timeout mutated to %s, want %s", time.Duration(timeout), time.Duration(existing))
			}
		})
	}
}

func TestWithRecipeTimeoutOverlayKeepsShorterRecipeTimeout(t *testing.T) {
	existing := recipecore.Duration(75 * time.Millisecond)
	rec := &recipecore.Recipe{RecipeImpl: &recipecore.RecipeSequence{
		RecipeMetadata: recipecore.RecipeMetadata{NodeMetadata: recipecore.NodeMetadata{ID: "root", Timeout: existing}},
	}}

	got := withRecipeTimeoutOverlay(rec, time.Second)
	if timeout := recipeRootTimeout(t, got); timeout != existing {
		t.Fatalf("overlay timeout = %s, want existing %s", time.Duration(timeout), time.Duration(existing))
	}
}

func recipeRootTimeout(t *testing.T, rec recipecore.Recipe) recipecore.Duration {
	t.Helper()
	switch typed := rec.RecipeImpl.(type) {
	case *recipecore.RecipeOp:
		return typed.RecipeMetadata.NodeMetadata.Timeout
	case *recipecore.RecipeSequence:
		return typed.RecipeMetadata.NodeMetadata.Timeout
	case *recipecore.RecipeState:
		return typed.RecipeMetadata.NodeMetadata.Timeout
	default:
		t.Fatalf("unsupported recipe type %T", typed)
		return 0
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
			{Type: "output_equals", Path: "fields.response", Value: "ok"},
			{Type: "artifact_exists", Path: "report.txt"},
		},
	}, ExecutionOptions{Mode: "isolated", ArtifactMode: "inline", ArtifactMaxBytes: 4})

	if resp.Status != "passed" {
		t.Fatalf("status = %q, failure reason: %s", resp.Status, resp.FailureReason)
	}
	if len(resp.Assertions) != 3 {
		t.Fatalf("expected 3 assertions, got %#v", resp.Assertions)
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

func TestRunCaseMockedInputOutputDefaults(t *testing.T) {
	resp := RunCase(context.Background(), HarnessOptions{}, "p1", TargetRecipe{
		Mode:   "inline_recipe",
		Format: "yaml",
		Content: `
version: '1.0'
id: input-defaults
op: input
inputs:
  form:
    title: Review
    fields:
      - id: decision
        question: Decision?
        type: dropdown
        options:
          - value: approve
      - id: selected
        question: Selected targets
        type: checkboxes
        options:
          - value: api
      - id: approved
        question: Approved?
        type: boolean
      - id: score
        question: Score
        type: linear_scale
        scale:
          min: 2
          max: 5
      - id: note
        question: Note
        type: short_answer
        default: none
`,
	}, Case{
		ID:   "input-defaults",
		Type: "recipe_case",
		Mocks: Mocks{
			Ops: []OpMock{{
				Match: OpMockMatch{Op: "input"},
				Behavior: MockBehavior{
					Mode: "return",
					Outputs: map[string]interface{}{
						"fields": map[string]interface{}{"approved": false},
					},
				},
			}},
		},
		Assertions: []Assertion{
			{Type: "output_equals", Path: "fields.decision", Value: ""},
			{Type: "output_equals", Path: "fields.selected", Value: []interface{}{}},
			{Type: "output_equals", Path: "fields.approved", Value: false},
			{Type: "output_equals", Path: "fields.score", Value: float64(2)},
			{Type: "output_equals", Path: "fields.note", Value: "none"},
		},
	}, ExecutionOptions{Mode: "isolated"})

	if resp.Status != "passed" {
		t.Fatalf("status = %q, failure reason: %s", resp.Status, resp.FailureReason)
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

func TestRunCaseAssertsVarsAndTransitionPayloadDiagnostics(t *testing.T) {
	const secret = "ghp_abcdefghijklmnopqrstuvwxyz123456"
	resp := RunCase(context.Background(), HarnessOptions{}, "p1", TargetRecipe{
		Mode:   "inline_recipe",
		Format: "yaml",
		Content: `
version: '1.0'
id: diagnostics
vars:
  feedback: ship it
  api_token: ghp_abcdefghijklmnopqrstuvwxyz123456
state:
  initial: review
  states:
    review:
      op: test_complex_input
      inputs:
        config:
          status: reviewed
      transitions:
        - to: requirements
          when: true
          payload:
            user_feedback: "${{ vars.feedback }}"
            api_token: "${{ vars.api_token }}"
    requirements:
      op: test_complex_input
      inputs:
        config:
          user_feedback: "${{ transition.payload.user_feedback }}"
outputs:
  result: "${{ state_output('requirements', 'config.user_feedback', 'missing') }}"
`,
	}, Case{
		ID:   "diagnostics",
		Type: "recipe_case",
		Mocks: Mocks{Ops: []OpMock{
			{Match: OpMockMatch{Op: "test_complex_input"}, Behavior: MockBehavior{Mode: "passthrough"}},
			{Match: OpMockMatch{Op: "test_complex_input"}, Behavior: MockBehavior{Mode: "passthrough"}},
		}},
		Assertions: []Assertion{
			{Type: "output_equals", Path: "result", Value: "ship it"},
			{Type: "var_equals", Scope: "recipe", Path: "feedback", Value: "ship it"},
			{Type: "var_equals", Scope: "recipe", Path: "api_token", Value: secret},
			{Type: "transition_payload_equals", FromState: "review", ToState: "requirements", Path: "user_feedback", Value: "ship it"},
			{Type: "transition_payload_equals", FromState: "review", ToState: "requirements", Path: "api_token", Value: secret},
		},
	}, ExecutionOptions{})

	if resp.Status != "passed" {
		t.Fatalf("status = %q, failure reason: %s", resp.Status, resp.FailureReason)
	}
	if len(resp.Diagnostics.Vars) == 0 {
		t.Fatal("expected rendered vars diagnostics")
	}
	redactedVar := resp.Diagnostics.Vars[0].Vars["api_token"]
	if redactedVar != "[REDACTED]" {
		t.Fatalf("redacted var = %#v, want [REDACTED]", redactedVar)
	}
	var foundPayload bool
	for _, tr := range resp.Diagnostics.Transitions {
		if tr.Selected && tr.FromState == "review" && tr.ToState == "requirements" {
			foundPayload = true
			if tr.Payload["api_token"] != "[REDACTED]" {
				t.Fatalf("redacted payload = %#v, want [REDACTED]", tr.Payload["api_token"])
			}
		}
	}
	if !foundPayload {
		t.Fatal("expected selected transition payload diagnostics")
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

func TestValidateCaseRunsSemanticValidation(t *testing.T) {
	validation := ValidateCase(context.Background(), HarnessOptions{}, "p1", TargetRecipe{
		Mode:   "inline_recipe",
		Format: "yaml",
		Content: `
version: '1.0'
id: semantic-validation
op: test_complex_input
inputs:
  config:
    value: "${{ missing_helper() }}"
`,
	}, Case{ID: "semantic-validation", Type: "recipe_case"})

	if validation.Valid {
		t.Fatal("expected semantic validation to fail")
	}
	if len(validation.Errors) == 0 || validation.Errors[0].Code != "semantic_validation" {
		t.Fatalf("expected semantic_validation error, got %#v", validation.Errors)
	}
	if !strings.Contains(validation.Errors[0].Message, "missing_helper") {
		t.Fatalf("expected helper name in error, got %q", validation.Errors[0].Message)
	}
}

func TestValidateCaseAllowsUnmockedRequiredInputFields(t *testing.T) {
	validation := ValidateCase(context.Background(), HarnessOptions{}, "p1", requiredDecisionInputTarget(), Case{
		ID:   "required-input-unmocked",
		Type: "recipe_case",
	})

	if !validation.Valid {
		t.Fatalf("validation errors: %+v", validation.Errors)
	}
}

func TestValidateCaseUsesReturnMockForRequiredInputFields(t *testing.T) {
	validation := ValidateCase(context.Background(), HarnessOptions{}, "p1", requiredDecisionInputTarget(), Case{
		ID:   "required-input-mocked",
		Type: "recipe_case",
		Mocks: Mocks{Ops: []OpMock{{
			Match: OpMockMatch{Op: "input"},
			Behavior: MockBehavior{
				Mode: "return",
				Outputs: map[string]interface{}{
					"fields": map[string]interface{}{
						"decision": "merge",
					},
				},
			},
		}}},
	})

	if !validation.Valid {
		t.Fatalf("validation errors: %+v", validation.Errors)
	}
}

func TestValidateCaseRejectsReturnMockMissingRequiredInputField(t *testing.T) {
	validation := ValidateCase(context.Background(), HarnessOptions{}, "p1", requiredDecisionInputTarget(), Case{
		ID:   "required-input-bad-mock",
		Type: "recipe_case",
		Mocks: Mocks{Ops: []OpMock{{
			Match: OpMockMatch{Op: "input"},
			Behavior: MockBehavior{
				Mode:    "return",
				Outputs: map[string]interface{}{"fields": map[string]interface{}{}},
			},
		}}},
	})

	if validation.Valid {
		t.Fatal("expected validation to fail")
	}
	if len(validation.Errors) == 0 || !strings.Contains(validation.Errors[0].Message, `required input field "decision" missing`) {
		t.Fatalf("expected required field error, got %#v", validation.Errors)
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

func requiredDecisionInputTarget() TargetRecipe {
	return TargetRecipe{
		Mode:   "inline_recipe",
		Format: "yaml",
		Content: `
version: '1.0'
id: required-input
op: input
inputs:
  form:
    title: Review
    fields:
      - id: decision
        question: Decision?
        type: multiple_choice
        required: true
        options:
          - value: merge
          - value: revise
`,
	}
}

func withRegisteredCoreOps(t *testing.T, ops ...coreops.RegisterableOp) {
	t.Helper()
	original := coreops.List()
	coreops.Replace(ops...)
	t.Cleanup(func() {
		coreops.Replace(original...)
	})
}

func mustLoadRecipe(t *testing.T, raw string) *recipecore.Recipe {
	t.Helper()
	rec, err := recipecore.LoadRecipeFromString([]byte(raw))
	if err != nil {
		t.Fatalf("load recipe: %v", err)
	}
	return rec
}
