package recipetest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	inputop "github.com/colony-2/c2j/pkg/input"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/stretchr/testify/require"
)

func TestInputFixtureActorDecoding(t *testing.T) {
	for _, raw := range []string{`null`, `true`, `1`, `"human"`, `[]`, `{}`, `{"id":"x"}`, `{"kind":"human"}`, `{"id":null,"kind":"human"}`, `{"id":"x","kind":null}`, `{"id":1,"kind":"human"}`, `{"id":"x","kind":[]}`, `{"id":"","kind":"human"}`, `{"id":"x","kind":""}`, `{"id":" \n\t","kind":"human"}`, `{"id":"x","kind":" \n\t"}`, `{"id":"x","kind":"human","extra":true}`, `{"id":"x","knd":"human"}`} {
		t.Run(raw, func(t *testing.T) {
			var fixture InputFixture
			err := json.Unmarshal([]byte(`{"node_path":"gate/review","actor":`+raw+`}`), &fixture)
			require.ErrorContains(t, err, "actor")
		})
	}
	var omitted InputFixture
	require.NoError(t, json.Unmarshal([]byte(`{"node_path":"gate/review","unrelated_future_field":true}`), &omitted))
	require.Nil(t, omitted.Actor)
	for _, raw := range []string{`{"Actor":null}`, `{"ACTOR":{"id":"x"}}`, `{"Actor":{"id":"x","kind":"human","extra":true}}`, `{"actor":{"id":"x","kind":"human"},"Actor":null}`} {
		var fixture InputFixture
		require.ErrorContains(t, json.Unmarshal([]byte(raw), &fixture), "actor")
	}
	for _, kind := range []string{"human", "test", "automation", "custom", " human "} {
		actor := inputop.Actor{ID: " reviewer ", Kind: kind}
		raw, err := json.Marshal(map[string]any{"actor": actor})
		require.NoError(t, err)
		var fixture InputFixture
		require.NoError(t, json.Unmarshal(raw, &fixture))
		require.Equal(t, &actor, fixture.Actor)
	}
}

func TestRuntimeActorValidationBeforeExecution(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	for _, actor := range []*inputop.Actor{nil, {ID: "x", Kind: "human"}, {}, {ID: "x"}, {Kind: "human"}, {ID: "x", Kind: " \t"}} {
		c := Case{ID: "invalid", Type: "integration_case", ExpectError: "actor", Runtime: &RuntimeCase{Responses: []InputFixture{{NodePath: "gate/review", Actor: actor}}}}
		issues := validateRuntimeCase(HarnessOptions{}, c)
		if actor == nil || actor.ID == "x" && actor.Kind == "human" {
			require.Empty(t, issues)
			continue
		}
		require.NotEmpty(t, issues)
		require.Contains(t, issues[0].Message, "response 0: actor.")
		// A valid recipe must stop before execution, and expect_error must never
		// turn an invalid fixture into a passing case.
		result := RunCase(context.Background(), HarnessOptions{WorkRoot: t.TempDir()}, "actors", actorTarget(false), c, ExecutionOptions{})
		require.Equal(t, "invalid", result.Status, "%+v", result)
		require.Equal(t, "validation_error", result.FailureCategory)
		require.Contains(t, result.FailureReason, "response 0: actor.")
		require.Nil(t, result.Runtime)
	}
}

func actorForm(structured bool) string {
	if structured {
		return `request: {title: Choose}
      response_schema:
        type: object
        required: [action]
        properties:
          action: {type: string, enum: [continue, stop]}
          actor: {type: object}`
	}
	return `kind: review
      title: Choose
      documents: {}
      fields:
      - id: action
        question: Continue or stop?
        type: multiple_choice
        required: true
        options:
        - {value: continue, label: Continue}
        - {value: stop, label: Stop}`
}

func actorTarget(structured bool) TargetRecipe {
	answer := "fields"
	if structured {
		answer = "response"
	}
	return TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `id: gate
version: "1"
sequence:
- id: review
  op: input
  inputs:
    form:
      ` + actorForm(structured) + `
- id: eligible
  when: sequence.review.outputs.receipt.actor.kind == 'human' && sequence.review.outputs.` + answer + `.action == 'continue'
  op: command_execution
  inputs: {run: unused}
outputs:
  receipt: '${{ sequence.review.outputs.receipt }}'
  eligible: '${{ "eligible" in sequence }}'
`}
}

func actorResponse(structured bool, actor *inputop.Actor, action string) InputFixture {
	r := InputFixture{NodePath: "gate/review", Actor: actor}
	if structured {
		r.Response = map[string]any{"action": action}
	} else {
		r.Fields = map[string]any{"action": action}
	}
	return r
}

func runActorCase(t *testing.T, target TargetRecipe, responses []InputFixture, expected string) CaseRunResult {
	t.Helper()
	var mocks Mocks
	if len(responses) > 0 && responses[0].Actor != nil && responses[0].Actor.Kind == "human" && expected == "" {
		mocks.Ops = []OpMock{{Match: OpMockMatch{NodePath: "gate/eligible"}, Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"continued": true}}}}
	}
	return RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: t.TempDir()}, "actors", target, Case{ID: t.Name(), Type: "integration_case", Runtime: &RuntimeCase{Responses: responses, ExpectError: expected}, Mocks: mocks}, ExecutionOptions{Timeout: "10s"})
}

func TestRuntimeResponseActorsAndHumanEligibility(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	for _, structured := range []bool{false, true} {
		for _, actor := range []*inputop.Actor{nil, {ID: "synthetic-reviewer", Kind: "human"}, {ID: "explicit-test", Kind: "test"}, {ID: "synthetic-agent", Kind: "automation"}, {ID: " custom ", Kind: " arbitrary "}} {
			t.Run(fmt.Sprintf("structured=%v/actor=%v", structured, actor), func(t *testing.T) {
				result := runActorCase(t, actorTarget(structured), []InputFixture{actorResponse(structured, actor, "continue")}, "")
				require.Equal(t, "passed", result.Status, "%+v", result)
				require.Len(t, result.Runtime.Reviews, 1)
				review := result.Runtime.Reviews[0]
				expected := inputop.Actor{ID: "recipe-test", Kind: "test"}
				if actor != nil {
					expected = *actor
				}
				require.NotNil(t, review.Receipt)
				require.Equal(t, expected, review.Receipt.Actor)
				require.NotEmpty(t, review.RequestID)
				require.Equal(t, review.RequestID, review.Receipt.RequestID)
				require.Equal(t, "0", review.Receipt.SubmissionID)
				require.False(t, review.Receipt.SubmittedAt.IsZero())
				var persisted inputop.Receipt
				raw, err := json.Marshal(result.Outputs["receipt"])
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &persisted))
				require.Equal(t, *review.Receipt, persisted)
				require.Equal(t, expected.Kind == "human", result.Outputs["eligible"])
				continuation := 0
				for _, call := range result.Runtime.Calls {
					if call.NodePath == "gate/eligible" {
						continuation++
					}
				}
				require.Equal(t, map[bool]int{true: 1, false: 0}[expected.Kind == "human"], continuation)
			})
		}
	}
}

func TestRuntimeHumanActorCannotBypassNativeValidation(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	for _, structured := range []bool{false, true} {
		for _, later := range []bool{false, true} {
			t.Run(fmt.Sprintf("structured=%v/later=%v", structured, later), func(t *testing.T) {
				responses := []InputFixture{actorResponse(structured, &inputop.Actor{ID: "reviewer", Kind: "human"}, "invalid")}
				if later {
					responses = append(responses, actorResponse(structured, nil, "continue"))
				}
				expected := "fields.action: invalid multiple_choice answer"
				if structured {
					expected = "response"
				}
				result := runActorCase(t, actorTarget(structured), responses, expected)
				if later {
					require.Equal(t, "failed", result.Status, "%+v", result)
					require.Equal(t, "response_unused", result.FailureCategory)
					require.Contains(t, result.FailureReason, "response 1")
					require.NotContains(t, result.FailureReason, "response 0")
				} else {
					require.Equal(t, "passed", result.Status, "%+v", result)
				}
				require.Empty(t, result.Runtime.Reviews)
				for _, call := range result.Runtime.Calls {
					require.NotEqual(t, "gate/eligible", call.NodePath)
				}
			})
		}
	}
}

func TestRuntimeActorIsSeparateFromAnswerData(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	for _, structured := range []bool{false, true} {
		response := actorResponse(structured, nil, "continue")
		if structured {
			response.Response.(map[string]any)["actor"] = map[string]any{"id": "spoofed", "kind": "human"}
		} else {
			response.Fields["actor"] = "human"
		}
		target := actorTarget(structured)
		if !structured {
			target.Content = strings.Replace(target.Content, "      fields:\n", "      fields:\n      - {id: actor, type: short_answer, question: Actor answer data}\n", 1)
		}
		result := runActorCase(t, target, []InputFixture{response}, "")
		require.Equal(t, "passed", result.Status, "%+v", result)
		require.Equal(t, inputop.Actor{ID: "recipe-test", Kind: "test"}, result.Runtime.Reviews[0].Receipt.Actor)
		require.Equal(t, false, result.Outputs["eligible"])
	}
}

func TestRuntimeActorsStayLocalAcrossResponses(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp())
	for _, structured := range []bool{false, true} {
		t.Run(fmt.Sprint(structured), func(t *testing.T) {
			// Re-enter the same review node twice, then answer a different node. Each
			// submission must select its own actor, including a later omitted actor.
			answer := "fields"
			if structured {
				answer = "response"
			}
			form := strings.ReplaceAll(actorForm(structured), "\n      ", "\n          ")
			target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `id: loop
state:
  initial: review
  states:
    review:
      op: input
      inputs:
        form:
          ` + form + `
      transitions:
      - {to: review, when: "states.review.outputs.` + answer + `.action == 'continue'"}
      - {to: final, when: true}
    final:
      op: input
      inputs:
        form:
          ` + form + `
outputs:
  receipt: '${{ states.final.outputs.receipt }}'
`}
			responses := []InputFixture{
				actorResponse(structured, &inputop.Actor{ID: "human", Kind: "human"}, "continue"),
				actorResponse(structured, &inputop.Actor{ID: "bot", Kind: "automation"}, "stop"),
				actorResponse(structured, nil, "stop"),
			}
			for i := range responses {
				responses[i].NodePath = "loop/review/input"
			}
			responses[2].NodePath = "loop/final/input"
			result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "actors", target, Case{ID: "local", Type: "integration_case", Runtime: &RuntimeCase{Responses: responses}}, ExecutionOptions{Timeout: "10s"})
			require.Equal(t, "passed", result.Status, "%+v", result)
			require.Len(t, result.Runtime.Reviews, 3)
			requests := map[string]bool{}
			for i, want := range []inputop.Actor{{ID: "human", Kind: "human"}, {ID: "bot", Kind: "automation"}, {ID: "recipe-test", Kind: "test"}} {
				receipt := result.Runtime.Reviews[i].Receipt
				require.Equal(t, want, receipt.Actor)
				require.Equal(t, fmt.Sprint(i), receipt.SubmissionID)
				require.False(t, requests[receipt.RequestID])
				requests[receipt.RequestID] = true
			}
		})
	}
}
