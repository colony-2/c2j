package recipetest

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/pkg/objects"
	"strings"
	"testing"

	inputop "github.com/colony-2/c2j/pkg/input"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/stretchr/testify/require"
)

const controlManifest = `name: neutral
shell: sh
run: 'exit 93'
input_schema:
  type: object
  properties:
    marker: {type: string, default: prepared}
output_schema:
  type: object
  required: [answer]
  properties:
    answer: {type: string}
`

func controlCase() Case {
	return Case{ID: "controlled", Type: "integration_case", Runtime: &RuntimeCase{
		Backend:  "sqlite",
		Cells:    map[string]CellFixture{"root": {Files: map[string]string{"echo/op.yaml": controlManifest, "candidate.txt": "retained candidate"}}},
		Controls: &RuntimeControls{Dispatch: &DispatchPolicy{Strict: true, Allow: []DispatchMatcher{{Kind: "task", Name: "recipe_cell_context"}, {Kind: "task", Name: "extension_execution:extension_execution", NodePath: "controls/answer", Cell: "root"}}}, Snapshot: &EffectSnapshotConfig{Files: []string{"candidate.txt"}}, Barriers: []PreparedBarrier{{ID: "answer", Match: PreparedMatch{Selector: "./echo", NodePath: "controls/answer", Cell: "root", Occurrence: 1, Attempt: 1}, Stdout: `{"output":{"answer":"ready"}}`}}, Actions: []ControlAction{{On: "answer.entered", Do: "release"}}},
	}}
}

func TestRuntimePreparedControls(t *testing.T) {
	withRegisteredCoreOps(t, extensions.GetExecutionOp())
	target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `id: controls
sequence:
- id: answer
  op: ./echo
outputs:
  answer: '${{ sequence.answer.outputs.answer }}'
`}
	for _, mode := range []string{"release", "cancel_job"} {
		t.Run(mode, func(t *testing.T) {
			c := controlCase()
			c.Runtime.Controls.Actions[0].Do = mode

			result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: t.TempDir()}, "native-controls", target, c, ExecutionOptions{Timeout: "20s"})
			require.NotNil(t, result.Runtime)
			raw, err := json.Marshal(result.Runtime)
			require.NoError(t, err)
			t.Log("CONTROL_REPORT", string(raw))
			require.Equal(t, "passed", result.Status, result.FailureReason)
			require.True(t, result.Runtime.Controls.Complete)
			events := result.Runtime.Controls.Events
			require.GreaterOrEqual(t, len(events), 5)
			entry := events[0]
			require.Equal(t, "entered", entry.Kind)
			require.NotEmpty(t, entry.JobID)
			require.NotEmpty(t, entry.InvocationID)
			require.Equal(t, 1, entry.Attempt)
			require.Positive(t, entry.ResultOrdinal)
			require.Equal(t, "prepared", entry.Inputs["marker"])
			kinds := []string{}
			for _, e := range events {
				kinds = append(kinds, e.Kind)
			}
			require.Contains(t, kinds, "exited")
			require.Equal(t, "drained", kinds[len(kinds)-1])
			if mode == "cancel_job" {
				require.True(t, result.Runtime.Controls.Preserved)
				baseline := result.Runtime.Controls.Baselines["answer"]
				final := result.Runtime.Controls.FinalSnapshot
				require.True(t, baseline.Complete)
				require.True(t, final.Complete)
				require.NotEmpty(t, baseline.Repositories)
				require.Equal(t, baseline.Repositories, final.Repositories)
				require.Equal(t, baseline.SuccessfulTasks, final.SuccessfulTasks)
				require.Equal(t, baseline.DispatchCount, final.DispatchCount)
				for _, repository := range final.Repositories {
					require.True(t, repository.Files["candidate.txt"].Exists)
					require.Equal(t, contentDigest([]byte("retained candidate")), repository.Files["candidate.txt"].Content.SHA256)
				}
				require.Contains(t, kinds, "cancel_job_accepted")
				require.Contains(t, kinds, "cancellation_observed")
				require.Empty(t, result.Outputs)
			} else {
				require.False(t, result.Runtime.Controls.Preserved)
				require.Equal(t, "ready", result.Outputs["answer"])
			}
		})
	}
}

func TestRuntimeControlValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Case)
	}{
		{"protected environment", func(c *Case) { c.Runtime.Controls.Barriers[0].CaptureEnv = []string{"C2J_CHILD_JOB_TOKEN"} }},
		{"response gate", func(c *Case) {
			c.Runtime.Responses = []InputFixture{{NodePath: "controls/review", After: "missing.exited"}}
		}},

		{"snapshot escape", func(c *Case) { c.Runtime.Controls.Snapshot.Files = []string{"../escape"} }},
		{"dispatch wildcard", func(c *Case) { c.Runtime.Controls.Dispatch.Allow[0].NodePath = "controls/*" }},
		{"dispatch limit", func(c *Case) { c.Runtime.Controls.Dispatch.MaxAttempts = 4097 }},
		{"completion escape", func(c *Case) {
			c.Runtime.Controls.Barriers[0].Objects = map[string]ObjectFixture{"next": {Type: "neutral/v1", Files: map[string]string{"../escape": "bad"}}}
		}},
		{"probe escape", func(c *Case) { c.Runtime.Controls.Barriers[0].ProbeTools = []string{"../tool"} }},
		{"backend", func(c *Case) { c.Runtime.Backend = "memory" }},
		{"selector", func(c *Case) { c.Runtime.Controls.Barriers[0].Match.Selector = "" }},
		{"wildcard", func(c *Case) { c.Runtime.Controls.Barriers[0].Match.NodePath = "controls/*" }},
		{"occurrence", func(c *Case) { c.Runtime.Controls.Barriers[0].Match.Occurrence = 0 }},
		{"attempt", func(c *Case) { c.Runtime.Controls.Barriers[0].Match.Attempt = 0 }},
		{"duplicate", func(c *Case) {
			c.Runtime.Controls.Barriers = append(c.Runtime.Controls.Barriers, c.Runtime.Controls.Barriers[0])
		}},
		{"ambiguous", func(c *Case) {
			b := c.Runtime.Controls.Barriers[0]
			b.ID = "other"
			c.Runtime.Controls.Barriers = append(c.Runtime.Controls.Barriers, b)
		}},
		{"event", func(c *Case) { c.Runtime.Controls.Actions[0].On = "missing.entered" }},
		{"action", func(c *Case) { c.Runtime.Controls.Actions[0].Do = "unknown" }},
		{"conflict", func(c *Case) {
			c.Runtime.Controls.Actions = append(c.Runtime.Controls.Actions, c.Runtime.Controls.Actions[0])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := controlCase()
			tc.mutate(&c)
			require.NotEmpty(t, validateRuntimeCase(HarnessOptions{}, c))
		})
	}
}

type neutralContinueInput struct {
	Action string `json:"action"`
}

func TestRuntimeControlledNativeReviews(t *testing.T) {
	sentinel := coreops.NewActivityMappedOpV2[neutralContinueInput, neutralContinueInput](coreops.OpMetadata{Type: "neutral_continue"}, func(_ coreops.OpDependencies, _ context.Context, in neutralContinueInput) (neutralContinueInput, error) {
		return in, nil
	})
	withRegisteredCoreOps(t, extensions.GetExecutionOp(), inputop.GetOp(), sentinel)
	target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `id: controls
sequence:
- id: question
  op: input
  inputs:
    form:
      kind: review
      title: Question
      documents: {}
      fields:
      - {id: question, type: short_answer, question: "What should I answer?", required: true}
- id: answer
  op: ./echo
- id: review
  op: input
  inputs:
    form:
      kind: review
      title: Answer
      documents: {}
      fields:
      - {id: action, type: short_answer, question: Continue, required: true}
- id: continuation
  op: neutral_continue
  inputs:
    action: '${{ sequence.review.outputs.fields.action }}'
outputs:
  answer: '${{ sequence.answer.outputs.answer }}'
`}
	for _, mode := range []string{"release", "cancel_job", "release_with_ordering"} {
		t.Run(mode, func(t *testing.T) {
			c := controlCase()
			action := mode
			if mode == "release_with_ordering" {
				action = "release"
			}
			c.Runtime.Controls.Actions[0].Do = action
			c.Runtime.Controls.Dispatch.Allow = append(c.Runtime.Controls.Dispatch.Allow,
				DispatchMatcher{Kind: "task", Name: "input:generate_form", NodePath: "controls/question"},
				DispatchMatcher{Kind: "task", Name: "input:generate_form", NodePath: "controls/review"},
				DispatchMatcher{Kind: "task", Name: "neutral_continue:neutral_continue", NodePath: "controls/continuation"})
			c.Runtime.Responses = []InputFixture{{NodePath: "controls/question", Fields: map[string]any{"question": "Explain the change"}}}
			if action == "release" {
				c.Runtime.Responses = append(c.Runtime.Responses, InputFixture{NodePath: "controls/review", After: "answer.exited", Fields: map[string]any{"action": "continue"}})
			}
			if mode == "release_with_ordering" {
				c.Runtime.Responses[1].AfterCall = &AfterCall{NodePath: "controls/review", Occurrence: 1}
			}
			result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: t.TempDir()}, "native-gates", target, c, ExecutionOptions{Timeout: "10s"})
			require.Equal(t, "passed", result.Status, "%+v", result)
			raw, err := json.Marshal(result.Runtime)
			require.NoError(t, err)
			t.Log("CONTROL_REPORT", string(raw))
			reviews := result.Runtime.Reviews
			require.NotEmpty(t, reviews)
			require.NotEmpty(t, reviews[0].RequestID)
			require.NotNil(t, reviews[0].Receipt)
			require.NotEmpty(t, reviews[0].Receipt.SubmissionID)
			require.Equal(t, reviews[0].RequestID, reviews[0].Receipt.RequestID)
			continuations := 0
			for _, call := range result.Runtime.Calls {
				if call.Op == "neutral_continue" {
					continuations++
				}
			}
			if action == "release" {
				require.Len(t, reviews, 2)
				require.NotEqual(t, reviews[0].RequestID, reviews[1].RequestID)
				require.NotEqual(t, reviews[0].Receipt.SubmissionID, reviews[1].Receipt.SubmissionID)
				require.Equal(t, 1, continuations)
				pending := 0
				for _, event := range result.Runtime.Controls.Events {
					if event.Kind != "review_pending" {
						continue
					}
					pending++
					require.Equal(t, reviews[1].RequestID, event.RequestID)
					require.NotNil(t, event.Form)
					for _, call := range event.Calls {
						require.NotEqual(t, "neutral_continue", call.Op)
					}
				}
				require.Equal(t, 1, pending)
			} else {
				require.Len(t, reviews, 1)
				require.Zero(t, continuations)
				require.Equal(t, "CANCELLED", result.Runtime.Controls.Final.Status)
			}
			passed, reason := evaluateAssertion(`controls.complete && controls.events.exists(e, e.kind == "drained")`, result.Outputs, nil, result.Status, result.Runtime)
			require.True(t, passed, reason)
			if mode == "release_with_ordering" {
				passed, reason = evaluateAssertion(`controls.complete && events.exists(e, e.kind == "gate_eligible") && events.exists(e, e.kind == "response_release" && e.node_path == "controls/review")`, result.Outputs, nil, result.Status, result.Runtime)
				require.True(t, passed, reason)
			}
		})
	}
}

func TestRuntimeControlsFailClosed(t *testing.T) {
	withRegisteredCoreOps(t, extensions.GetExecutionOp())
	target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `id: controls
sequence:
- id: answer
  op: ./echo
outputs: {}
`}
	for _, tc := range []struct {
		name    string
		mutate  func(*Case)
		timeout string
	}{
		{"missing match", func(c *Case) {
			c.Runtime.Controls.Barriers[0].Match.NodePath = "controls/missing"
			c.Runtime.ExpectError = "no runtime fixture"
		}, "5s"},
		{"missing occurrence", func(c *Case) {
			c.Runtime.Controls.Barriers[0].Match.Occurrence = 2
			c.Runtime.ExpectError = "no runtime fixture"
		}, "5s"},
		{"wrong attempt", func(c *Case) {
			c.Runtime.Controls.Barriers[0].Match.Attempt = 2
			c.Runtime.ExpectError = "unexpected retry"
		}, "5s"},
		{"unreleased", func(c *Case) { c.Runtime.Controls.Actions = nil; c.Runtime.ExpectError = "deadline" }, "1s"},
		{"incomplete entry", func(c *Case) {
			c.Runtime.Controls.MaxEvidenceBytes = 32
			c.Runtime.ExpectError = "evidence exceeds limit"
		}, "5s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := controlCase()
			tc.mutate(&c)
			result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: t.TempDir()}, "negative-controls", target, c, ExecutionOptions{Timeout: tc.timeout})
			require.Equal(t, "failed", result.Status, "%+v", result)
			require.Equal(t, "control_failure", result.FailureCategory)
			require.NotNil(t, result.Runtime.Controls)
			require.False(t, result.Runtime.Controls.Complete)
			require.NotEmpty(t, result.Runtime.Controls.Errors)
		})
	}
}

func TestRuntimePreparedApplicationFailureAndRetry(t *testing.T) {
	withRegisteredCoreOps(t, extensions.GetExecutionOp())
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			policy := ""
			if retry {
				policy = "  retry: {maximum_attempts: 2, initial_interval: 1ms}\n"
			}
			target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: "id: controls\nsequence:\n- id: answer\n" + policy + "  op: ./echo\noutputs: {}\n"}
			c := controlCase()
			c.Runtime.Controls.Barriers[0].Stdout = ""
			c.Runtime.Controls.Barriers[0].Error = "ordinary application failure"
			c.Runtime.ExpectError = "ordinary application failure"
			result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: t.TempDir()}, "native-errors", target, c, ExecutionOptions{Timeout: "5s"})
			if retry {
				require.Equal(t, "failed", result.Status, "%+v", result)
				require.Equal(t, "control_failure", result.FailureCategory)
				require.False(t, result.Runtime.Controls.Complete)
			} else {
				require.Equal(t, "passed", result.Status, "%+v", result)
				require.True(t, result.Runtime.Controls.Complete)
			}
			require.NotEqual(t, "cancellation", result.Runtime.Controls.Final.FailureKind)
			for _, event := range result.Runtime.Controls.Events {
				require.False(t, event.NativeCancellation)
			}
		})
	}
}

func TestRuntimePreparedApplicationCatch(t *testing.T) {
	withRegisteredCoreOps(t, extensions.GetExecutionOp())
	target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `id: controls
sequence:
- id: answer
  op: ./echo
  catch:
  - id: ordinary
    when: failure_message_contains(failure, "ordinary application failure")
    continue:
      outputs: {answer: recovered}
outputs:
  answer: '${{ sequence.answer.outputs.answer }}'
`}
	c := controlCase()
	c.Runtime.Controls.Barriers[0].Stdout = ""
	c.Runtime.Controls.Barriers[0].Error = "ordinary application failure"
	result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: t.TempDir()}, "ordinary-catch", target, c, ExecutionOptions{Timeout: "5s"})
	require.Equal(t, "passed", result.Status, result.FailureReason)
	require.Equal(t, "recovered", result.Outputs["answer"])
	require.True(t, result.Runtime.Controls.Complete)
	require.NotEqual(t, "cancellation", result.Runtime.Controls.Final.FailureKind)
	for _, event := range result.Runtime.Controls.Events {
		require.False(t, event.NativeCancellation)
	}
}

func TestRuntimeEffectLimitsFailClosed(t *testing.T) {
	withRegisteredCoreOps(t, extensions.GetExecutionOp())
	target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: "id: controls\nsequence:\n- id: answer\n  op: ./echo\noutputs: {}\n"}
	for _, kind := range []string{"snapshot", "file"} {
		t.Run(kind, func(t *testing.T) {
			c := controlCase()
			if kind == "snapshot" {
				c.Runtime.Controls.Snapshot.MaxBytes = 16
			} else {
				c.Runtime.Controls.Snapshot.MaxArtifactBytes = 1
			}
			c.Runtime.ExpectError = "incomplete"
			result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: t.TempDir()}, "effect-limit", target, c, ExecutionOptions{Timeout: "2s"})
			require.Equal(t, "failed", result.Status)
			require.Equal(t, "control_failure", result.FailureCategory)
			require.False(t, result.Runtime.Controls.Baselines["answer"].Complete)
			require.False(t, result.Runtime.Controls.Complete)
		})
	}
}

func TestNativePreparationAndOutputFailures(t *testing.T) {
	withRegisteredCoreOps(t, extensions.GetExecutionOp())
	for _, mode := range []string{"invalid input", "missing object", "malformed output", "invalid output"} {
		t.Run(mode, func(t *testing.T) {
			c := controlCase()
			content := "id: controls\nsequence:\n- id: answer\n  op: ./echo\n"
			entered := false
			switch mode {
			case "invalid input":
				content += "  inputs: {marker: 42}\n"
			case "missing object":
				cell := c.Runtime.Cells["root"]
				cell.Files["echo/op.yaml"] = controlManifest + "\n"
				cell.Files["echo/op.yaml"] = strings.Replace(cell.Files["echo/op.yaml"], "marker: {type: string, default: prepared}", "marker: {type: string, default: prepared}\n    session: {type: object, x-c2j-object-type: neutral.session/v1}", 1)
				c.Runtime.Cells["root"] = cell
				missing := objects.ValidationRef("neutral.session/v1")
				missing["tenant_id"] = "preparation-negative"
				ref, err := json.Marshal(missing)
				require.NoError(t, err)
				content += "  inputs:\n    session: " + string(ref) + "\n"
			case "malformed output":
				entered = true
				c.Runtime.Controls.Barriers[0].Stdout = "{"
			case "invalid output":
				entered = true
				c.Runtime.Controls.Barriers[0].Stdout = `{"output":{"answer":42}}`
			}
			result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: t.TempDir()}, "preparation-negative", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: content}, c, ExecutionOptions{Timeout: "5s"})
			require.Equal(t, "failed", result.Status, result.FailureReason)
			actual := false
			if result.Runtime != nil && result.Runtime.Controls != nil {
				for _, event := range result.Runtime.Controls.Events {
					if event.Kind == "entered" {
						actual = true
					}
				}
			}
			require.Equal(t, entered, actual)
		})
	}
}
