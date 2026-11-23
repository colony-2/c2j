package recipetest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	inputop "github.com/colony-2/c2j/pkg/input"
	coreops "github.com/colony-2/c2j/pkg/ops"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/stretchr/testify/require"
)

func responsePersistenceCase(structured bool, stage string) (Case, TargetRecipe) {
	form := `kind: review
      title: Retained diagnosis
      documents: {}
      fields:
      - id: context
        type: short_answer
        required: true
        question: '${{ jq({"original": sequence.original.outputs.cause, "recovery": sequence.recovery.outputs.cause}, "tojson") }}' `
	if structured {
		form = `request:
        title: Retained diagnosis
        original: '${{ sequence.original.outputs.cause }}'
        recovery: '${{ sequence.recovery.outputs.cause }}'
      response_schema:
        type: object
        required: [context]
        properties: {context: {type: string}}`
	}
	target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `schema_version: 2
id: persistence
version: "1"
sequence:
- id: original
  op: command_execution
  inputs: {run: original}
  catch:
  - when: true
    continue:
      outputs:
        cause: {kind: '${{ failure.kind }}', message: '${{ failure.message }}'}
- id: recovery
  op: command_execution
  inputs: {run: recovery}
  catch:
  - when: true
    continue:
      outputs:
        cause: {kind: '${{ failure.kind }}', message: '${{ failure.message }}'}
- id: reply
  op: input
  inputs:
    form:
      ` + form + `
  catch:
  - when: true
    continue:
      outputs: {caught: true}
- id: continuation
  op: command_execution
  inputs: {run: unused}
outputs:
  answer: '${{ sequence.reply.outputs }}'
  original: '${{ sequence.original.outputs.cause }}'
  recovery: '${{ sequence.recovery.outputs.cause }}'
`}
	q := TaskSelection{TaskType: "input:collect_user_input", NodePath: "persistence/reply", Attempt: 1, Occurrence: 1}
	response := InputFixture{NodePath: "persistence/reply", Cell: "root", Actor: &inputop.Actor{ID: "synthetic-human", Kind: "human"}, Submission: &ResponseSubmission{ID: "faulted", Pending: ResponsePending{Job: "root", Task: q}, AfterAll: []string{"stop"}}}
	if structured {
		response.Response = map[string]any{"context": "diagnosis"}
	} else {
		response.Fields = map[string]any{"context": "diagnosis"}
	}
	s := &Scenario{Jobs: map[string]ScenarioJob{"root": {Root: true}}, Outcomes: map[string]ScenarioOutcome{"root": {Status: "completed"}}, Events: []ScenarioEvent{{ID: "pending", Kind: "response", Response: "faulted", Phase: "pending"}, {ID: "attempted", Kind: "response", Response: "faulted", Phase: "attempted"}}, Actions: []ScenarioAction{{ID: "stop", Do: "stop_worker", AfterAll: []string{"pending"}}}, Retain: []ScenarioRetention{{ID: "frozen-generation", Job: "root", Task: TaskSelection{TaskType: "input:generate_form", NodePath: "persistence/reply", Attempt: 1, Occurrence: 1}}, {ID: "original-input", Job: "root", Part: "input", Task: TaskSelection{TaskType: "command_execution:command_execution", NodePath: "persistence/original", Attempt: 1, Occurrence: 1}}, {ID: "recovery-input", Job: "root", Part: "input", Task: TaskSelection{TaskType: "command_execution:command_execution", NodePath: "persistence/recovery", Attempt: 1, Occurrence: 1}}}}
	c := Case{ID: stage, Type: "integration_case", Runtime: &RuntimeCase{Backend: "sqlite", Responses: []InputFixture{response}, Controls: &RuntimeControls{Scenario: s}}, Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{NodePath: "persistence/original"}, Behavior: MockBehavior{Mode: "fail", Error: &TestError{Message: "ORIGINAL_NATIVE_CAUSE"}}}, {Match: OpMockMatch{NodePath: "persistence/recovery"}, Behavior: MockBehavior{Mode: "fail", Error: &TestError{Message: "RECOVERY_NATIVE_CAUSE"}}}}}}
	if stage == "cancellation" {
		s.Outcomes["root"] = ScenarioOutcome{Status: "cancelled"}
		s.Actions = append(s.Actions, ScenarioAction{ID: "cancel", Do: "cancel_job", Target: "root", AfterAll: []string{"stop"}})
		response.Submission.AfterAll = []string{"cancel"}
		response.Submission.ExpectError = &ResponseExpectedError{Kind: "storage"}
		c.Runtime.Responses[0] = response
		c.Assertions = []Assertion{{Type: "op_call_count", NodePath: "persistence/continuation", Value: 0}}
		return c, target
	}
	c.Mocks.Ops = append(c.Mocks.Ops, OpMock{Match: OpMockMatch{NodePath: "persistence/continuation"}, Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"ok": true}}})
	c.Assertions = []Assertion{{Type: "op_call_count", NodePath: "persistence/continuation", Value: 1}}
	switch stage {
	case "claim", "response_output", "settlement":
		response.Submission.Fault = stage
		kind := "storage"
		if stage == "response_output" {
			kind = "input_not_pending"
		}
		response.Submission.ExpectError = &ResponseExpectedError{Kind: kind}
		c.Runtime.Responses[0] = response
	case "healthy":
	default:
		panic(stage)
	}
	if stage == "response_output" || stage == "settlement" {
		s.Actions = append(s.Actions, ScenarioAction{ID: "expire", Do: "expire_response_lease", Target: "faulted", AfterAll: []string{"attempted"}}, ScenarioAction{ID: "reopen", Do: "reopen_runtime", AfterAll: []string{"expire"}}, ScenarioAction{ID: "start", Do: "start_worker", AfterAll: []string{"reopen"}})
	}
	if stage == "claim" || stage == "response_output" {
		s.Events = append(s.Events, ScenarioEvent{ID: "recovered", Kind: "response", Response: "faulted", Phase: "recovered", Status: "pending"})
		healthy := response
		healthy.Submission = &ResponseSubmission{ID: "healthy", Pending: response.Submission.Pending, AfterAll: []string{"recovered"}}
		if stage == "response_output" {
			s.Actions = append(s.Actions, ScenarioAction{ID: "stop-again", Do: "stop_worker", AfterAll: []string{"recovered"}})
			healthy.Submission.AfterAll = []string{"stop-again"}
		}
		c.Runtime.Responses = append(c.Runtime.Responses, healthy)
		s.Events = append(s.Events, ScenarioEvent{ID: "healthy-attempted", Kind: "response", Response: "healthy", Phase: "attempted"})
		s.Actions = append(s.Actions, ScenarioAction{ID: "finish", Do: "start_worker", AfterAll: []string{"healthy-attempted"}})
	} else if stage == "healthy" {
		s.Actions = append(s.Actions, ScenarioAction{ID: "finish", Do: "start_worker", AfterAll: []string{"attempted"}})
	} else {
		s.Events = append(s.Events, ScenarioEvent{ID: "recovered", Kind: "response", Response: "faulted", Phase: "recovered", Status: "completed"})
		s.Actions = append(s.Actions, ScenarioAction{ID: "stop-complete", Do: "stop_worker", AfterAll: []string{"recovered"}}, ScenarioAction{ID: "reopen-complete", Do: "reopen_runtime", AfterAll: []string{"stop-complete"}})
		s.Events = append(s.Events, ScenarioEvent{ID: "reobserved", AfterAll: []string{"reopen-complete"}, Kind: "response", Response: "faulted", Phase: "recovered", Status: "completed"})
		// Gate this checkpoint on reopening so it records a distinct native read.
	}
	return c, target
}
func runResponsePersistence(t *testing.T, c Case, target TargetRecipe) CaseRunResult {
	t.Helper()
	return RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: t.TempDir()}, "response-persistence", target, c, ExecutionOptions{Timeout: "20s"})
}
func TestResponsePersistenceNativeBoundaries(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	for _, structured := range []bool{false, true} {
		for _, stage := range []string{"claim", "response_output", "settlement", "healthy", "cancellation"} {
			t.Run(fmt.Sprintf("%t/%s", structured, stage), func(t *testing.T) {
				c, target := responsePersistenceCase(structured, stage)
				result := runResponsePersistence(t, c, target)
				raw, _ := json.MarshalIndent(result, "", "  ")
				require.Equal(t, "passed", result.Status, "%s", raw)
				a := result.Runtime.Submissions[0]
				require.True(t, a.Complete)
				require.True(t, a.Attempted)
				require.True(t, a.Matched)
				require.NotEmpty(t, a.Pending.Task.InvocationID)
				require.NotEmpty(t, a.Pending.Task.ScopeID)
				require.Equal(t, "0", a.SubmissionID)
				require.Equal(t, 1, a.Pending.Task.Attempt)
				var causes map[string]any
				if structured {
					raw, _ := json.Marshal(a.Pending.Form.Request)
					require.NoError(t, json.Unmarshal(raw, &causes))
					delete(causes, "title")
				} else {
					require.NoError(t, json.Unmarshal([]byte(a.Pending.Form.Fields[0].Question), &causes))
				}
				require.Equal(t, map[string]any{"original": map[string]any{"kind": "task_error", "message": "ORIGINAL_NATIVE_CAUSE"}, "recovery": map[string]any{"kind": "task_error", "message": "RECOVERY_NATIVE_CAUSE"}}, causes)
				retained := result.Runtime.Controls.Scenario.Retained["frozen-generation"]
				retainedBytes, e := base64.StdEncoding.DecodeString(retained.Content.ContentBase64)
				require.NoError(t, e)
				var env coretask.OutputEnvelope
				require.NoError(t, json.Unmarshal(retainedBytes, &env))
				var nativeForm workerops.ActivityInvocationOutput
				require.NoError(t, env.DecodePayload(&nativeForm))
				nativeRaw, e := json.Marshal(nativeForm.OpOutput)
				require.NoError(t, e)
				var frozen inputop.InputForm
				require.NoError(t, json.Unmarshal(nativeRaw, &frozen))
				require.Equal(t, a.Pending.Form, frozen)
				require.Equal(t, a.Pending.FormSHA256, formDigest(frozen))

				require.True(t, a.Before.InputUnchanged)
				require.True(t, a.After.InputUnchanged)
				require.True(t, result.Runtime.Controls.Scenario.Complete)
				require.Len(t, result.Runtime.Controls.Scenario.Retained, 3)
				switch stage {
				case "claim", "response_output":
					require.Nil(t, a.ReturnedReceipt)
					require.Equal(t, "absent", a.Durable.Status)
					require.Len(t, a.Checkpoints, 1)
					checkpoint := a.Checkpoints[0]
					require.Zero(t, checkpoint.State.Dispatches)
					require.True(t, checkpoint.State.Actionable)
					require.Nil(t, checkpoint.State.Lease)
					if stage == "claim" {
						require.Equal(t, "not_required", checkpoint.RecoveryMode)
						require.Nil(t, checkpoint.Replay)
						require.Equal(t, a.After.Session, checkpoint.State.Session)
					} else {
						require.Equal(t, "worker_replay", checkpoint.RecoveryMode)
						require.NotNil(t, checkpoint.Replay)
						require.True(t, checkpoint.Replay.Suspended)
						require.Greater(t, checkpoint.Replay.Session, a.After.Session)
						require.NotNil(t, a.After.Lease)
						require.False(t, a.After.Actionable)
					}
					require.Len(t, result.Runtime.Submissions, 2)
					healthy := result.Runtime.Submissions[1]
					require.True(t, healthy.Matched)
					require.Equal(t, a.Pending.RequestID, healthy.Pending.RequestID)
					require.Equal(t, healthy.ReturnedReceipt, healthy.Durable.Receipt)
					require.Len(t, result.Runtime.Reviews, 1)
				case "settlement":
					require.Nil(t, a.ReturnedReceipt)
					require.Equal(t, "present", a.Durable.Status)
					require.NotNil(t, a.After.Lease)
					require.Len(t, a.Checkpoints, 2)
					for _, checkpoint := range a.Checkpoints {
						require.Equal(t, a.Durable.Receipt, checkpoint.State.Durable.Receipt)
						require.Equal(t, 1, checkpoint.State.Dispatches)
					}
					require.Empty(t, result.Runtime.Reviews)
					require.Len(t, result.Runtime.Submissions, 1)
				case "healthy":
					require.Equal(t, a.ReturnedReceipt, a.Durable.Receipt)
					require.Len(t, result.Runtime.Reviews, 1)
				case "cancellation":
					require.Equal(t, "CANCELLED", a.After.Status)
					require.False(t, a.After.Actionable)
					require.True(t, a.After.HandlePresent)
					require.Nil(t, a.ReturnedReceipt)
					require.Equal(t, "absent", a.Durable.Status)
					require.Zero(t, a.After.Dispatches)
				}
				if a.Fault != nil {
					require.True(t, a.Fault.Armed)
					require.EqualValues(t, 1, a.Fault.Hits)
					require.True(t, a.Fault.Removed)
				}
				require.NotContains(t, string(raw), `"caught": true`)
			})
		}
	}
}
func TestResponseSubmissionUnexpectedErrorsRetainEvidence(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	for _, mode := range []string{"unexpected-error", "mismatch", "unexpected-success", "unused-fault"} {
		t.Run(mode, func(t *testing.T) {
			c, target := responsePersistenceCase(false, "claim")
			s := c.Runtime.Responses[0].Submission
			switch mode {
			case "unexpected-error":
				s.ExpectError = nil
			case "mismatch":
				s.ExpectError = &ResponseExpectedError{Kind: "validation"}
			case "unexpected-success":
				s.Fault = ""
			case "unused-fault":
				c.Runtime.Responses[0].Fields = map[string]any{}
			}
			result := runResponsePersistence(t, c, target)
			require.Equal(t, "failed", result.Status)
			require.NotEmpty(t, result.Runtime.Submissions)
			a := result.Runtime.Submissions[0]
			require.True(t, a.Attempted)
			require.NotNil(t, a.After)
			require.NotEmpty(t, a.Pending.RequestID)
			if mode != "unexpected-success" {
				require.NotEmpty(t, a.APIError)
				require.Nil(t, a.ReturnedReceipt)
				require.Equal(t, "absent", a.Durable.Status)
			} else {
				require.NotNil(t, a.ReturnedReceipt)
				require.Equal(t, "present", a.Durable.Status)
			}
			if a.Fault != nil {
				require.True(t, a.Fault.Removed)
			}
			require.False(t, a.Complete)
		})
	}
}
func TestResponseSubmissionGateDeadline(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	c, target := responsePersistenceCase(false, "healthy")
	c.Runtime.Controls.Scenario.Events = append(c.Runtime.Controls.Scenario.Events, ScenarioEvent{ID: "never", Kind: "settled", Job: "root", Status: "completed"})
	c.Runtime.Responses[0].Submission.AfterAll = []string{"never"}
	result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), FixtureRoot: t.TempDir(), WorkRoot: t.TempDir()}, "gate-deadline", target, c, ExecutionOptions{Timeout: "2s"})
	require.NotEqual(t, "passed", result.Status)
	require.False(t, result.Runtime.Submissions[0].Attempted)
	require.NotEqual(t, "CANCELLED", result.Runtime.Controls.Final.Status)
	require.True(t, strings.Contains(result.FailureReason, "response") || strings.Contains(result.FailureReason, "event"), result.FailureReason)
}

func TestResponseSubmissionLegacyCannotBypassGate(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	for _, structured := range []bool{false, true} {
		t.Run(fmt.Sprint(structured), func(t *testing.T) {
			c, target := responsePersistenceCase(structured, "healthy")
			s := c.Runtime.Controls.Scenario
			s.Events = append(s.Events, ScenarioEvent{ID: "never", Kind: "settled", Job: "root", Status: "completed"})
			c.Runtime.Responses[0].Submission.AfterAll = []string{"never"}
			legacy := c.Runtime.Responses[0]
			legacy.Submission = nil
			c.Runtime.Responses = append(c.Runtime.Responses, legacy)
			result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), FixtureRoot: t.TempDir(), WorkRoot: t.TempDir()}, "gated-collision", target, c, ExecutionOptions{Timeout: "2s"})
			require.NotEqual(t, "passed", result.Status)
			require.Contains(t, result.FailureReason, "context deadline exceeded")
			require.Len(t, result.Runtime.Submissions, 1)
			require.False(t, result.Runtime.Submissions[0].Attempted)
			require.Empty(t, result.Runtime.Reviews)
			require.NotEqual(t, "CANCELLED", result.Runtime.Controls.Final.Status)
			for _, call := range result.Runtime.Calls {
				require.NotEqual(t, "persistence/continuation", call.NodePath)
			}
		})
	}
}

func TestResponsePersistenceNamedChildIsolation(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	c, target := responsePersistenceCase(false, "response_output")
	childRecipe := target.Content
	target.Content = strings.Replace(target.Content, "sequence:\n", "sequence:\n- id: spawn\n  op: command_execution\n  inputs: {run: unused}\n", 1)
	c.Runtime.Cells = map[string]CellFixture{"root": {}}
	for i := range c.Mocks.Ops {
		c.Mocks.Ops[i].Repeat = true
	}
	c.Mocks.Ops = append(c.Mocks.Ops, OpMock{Match: OpMockMatch{NodePath: "persistence/spawn"}, Behavior: MockBehavior{Mode: "return", Effects: &FixtureEffects{Children: []ChildFixture{{Bind: "child", Recipe: "child.yaml", Cell: "root"}}}}})
	child := c.Runtime.Responses[0]
	child.Actor = &inputop.Actor{ID: "child-human", Kind: "human"}
	child.Submission = &ResponseSubmission{ID: "child-answer", Pending: ResponsePending{Job: "child", Task: child.Submission.Pending.Task}, AfterAll: []string{"stop"}}
	c.Runtime.Responses = append(c.Runtime.Responses, child)
	s := c.Runtime.Controls.Scenario
	s.Jobs["child"] = ScenarioJob{ChildBinding: "child"}
	s.Outcomes["child"] = ScenarioOutcome{Status: "completed"}
	s.Events = append(s.Events, ScenarioEvent{ID: "child-pending", Kind: "response", Response: "child-answer", Phase: "pending"}, ScenarioEvent{ID: "child-attempted", Kind: "response", Response: "child-answer", Phase: "attempted"})
	s.Actions[0].AfterAll = append(s.Actions[0].AfterAll, "child-pending")
	s.Events = append(s.Events, ScenarioEvent{ID: "child-completed", Kind: "settled", Job: "child", Status: "completed"})
	for i := range s.Actions {
		if s.Actions[i].ID == "start" {
			s.Actions[i].AfterAll = append(s.Actions[i].AfterAll, "child-attempted")
		}
		if s.Actions[i].ID == "stop-again" {
			s.Actions[i].AfterAll = append(s.Actions[i].AfterAll, "child-completed")
		}
	}
	c.Assertions = []Assertion{{Type: "op_call_count", NodePath: "persistence/continuation", Value: 2}}
	fixtures := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fixtures, "child.yaml"), []byte(childRecipe), 0600))
	result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), FixtureRoot: fixtures, WorkRoot: t.TempDir()}, "child-isolation", target, c, ExecutionOptions{Timeout: "20s"})
	raw, _ := json.MarshalIndent(result, "", "  ")
	require.Equal(t, "passed", result.Status, "%s", raw)
	byID := map[string]SubmissionAttempt{}
	for _, a := range result.Runtime.Submissions {
		byID[a.ID] = a
	}
	root, answer := byID["faulted"], byID["child-answer"]
	require.NotEqual(t, root.Pending.JobID, answer.Pending.JobID)
	require.Equal(t, root.Pending.Task.NodePath, answer.Pending.Task.NodePath)
	require.Equal(t, "absent", root.Durable.Status)
	require.Equal(t, "present", answer.Durable.Status)
	require.Equal(t, "child-human", answer.Durable.Receipt.Actor.ID)
	require.EqualValues(t, 1, root.Fault.Hits)
	require.Zero(t, root.Checkpoints[0].State.Dispatches)
}

func TestResponsePersistenceRepeatedPendingOccurrences(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	for _, structured := range []bool{false, true} {
		t.Run(fmt.Sprint(structured), func(t *testing.T) {
			c, target := repeatedResponsePersistenceCase(structured)
			result := runResponsePersistence(t, c, target)
			raw, _ := json.MarshalIndent(result, "", "  ")
			require.Equal(t, "passed", result.Status, "%s\n%s", raw, target.Content)
			require.Len(t, result.Runtime.Submissions, 2)
			a, b := result.Runtime.Submissions[0], result.Runtime.Submissions[1]
			require.Equal(t, 1, a.Pending.Task.Occurrence)
			require.Equal(t, 2, b.Pending.Task.Occurrence)
			require.NotEqual(t, a.Pending.Task.InvocationID, b.Pending.Task.InvocationID)
			require.NotEqual(t, a.Pending.RequestID, b.Pending.RequestID)
			require.Less(t, a.Pending.WaitOrdinal, b.Pending.WaitOrdinal)
			require.Equal(t, "0", a.Durable.Receipt.SubmissionID)
			require.Equal(t, "1", b.Durable.Receipt.SubmissionID)
		})
	}
}

func repeatedResponsePersistenceCase(structured bool) (Case, TargetRecipe) {
	c, target := responsePersistenceCase(structured, "healthy")
	target = repeatResponsePersistenceTarget(target, "0")
	path := "persistence/repeat/reply/input"
	first := c.Runtime.Responses[0]
	first.NodePath = path
	first.Submission.Pending.Task.NodePath = path
	second := first
	second.Submission = &ResponseSubmission{ID: "second", Pending: ResponsePending{Job: "root", Task: first.Submission.Pending.Task}, AfterAll: []string{"stop-second"}}
	second.Submission.Pending.Task.Occurrence = 2
	c.Runtime.Responses = []InputFixture{first, second}
	s := c.Runtime.Controls.Scenario
	s.Retain = nil
	s.Events = append(s.Events, ScenarioEvent{ID: "second-pending", Kind: "response", Response: "second", Phase: "pending"}, ScenarioEvent{ID: "second-attempted", Kind: "response", Response: "second", Phase: "attempted"})
	s.Actions = append(s.Actions, ScenarioAction{ID: "stop-second", Do: "stop_worker", AfterAll: []string{"second-pending"}}, ScenarioAction{ID: "reopen-second", Do: "reopen_runtime", AfterAll: []string{"second-attempted"}}, ScenarioAction{ID: "start-second", Do: "start_worker", AfterAll: []string{"reopen-second"}})
	return c, target
}

func repeatResponsePersistenceTarget(target TargetRecipe, firstSubmissionID string) TargetRecipe {
	// Reenter the same authored input node through native state activations.
	begin := strings.Index(target.Content, "- id: reply\n")
	end := strings.Index(target.Content, "- id: continuation\n")
	reply := target.Content[begin:end]
	reply = strings.Replace(reply, "- id: reply\n", "", 1)
	reply = strings.ReplaceAll(reply, "sequence.original.outputs.cause", `{"kind":"task_error","message":"ORIGINAL_NATIVE_CAUSE"}`)
	reply = strings.ReplaceAll(reply, "sequence.recovery.outputs.cause", `{"kind":"task_error","message":"RECOVERY_NATIVE_CAUSE"}`)
	reply = strings.ReplaceAll(reply, `RECOVERY_NATIVE_CAUSE"}}`, `RECOVERY_NATIVE_CAUSE"} }`)
	// Preserve the shared path by making the state itself the input operation.
	lines := strings.Split(strings.TrimSuffix(reply, "\n"), "\n")
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	target.Content = target.Content[:begin] + "- id: repeat\n  state:\n    initial: reply\n    states:\n      reply:\n" + strings.Join(func() []string {
		x := make([]string, len(lines))
		for i, l := range lines {
			x[i] = "    " + l
		}
		return x
	}(), "\n") + "\n        transitions:\n        - to: reply\n          when: states.reply.outputs.receipt.submission_id == '" + firstSubmissionID + "'\n" + target.Content[end:]
	target.Content = strings.Replace(target.Content, "answer: '${{ sequence.reply.outputs }}'", "answer: {}", 1)
	return target
}

func TestResponsePersistenceLegacyIsolation(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	for _, structured := range []bool{false, true} {
		for _, stage := range []string{"claim", "response_output", "healthy"} {
			t.Run(fmt.Sprintf("%t/%s", structured, stage), func(t *testing.T) {
				c, target := responsePersistenceCase(structured, stage)
				controlledCount := len(c.Runtime.Responses)
				target = repeatResponsePersistenceTarget(target, fmt.Sprint(controlledCount-1))
				path := "persistence/repeat/reply/input"
				for i := range c.Runtime.Responses {
					c.Runtime.Responses[i].NodePath = path
					c.Runtime.Responses[i].Submission.Pending.Task.NodePath = path
				}
				c.Runtime.Controls.Scenario.Retain[0].Task.NodePath = path
				// The legacy fixture matches the controlled occurrence's node/cell,
				// but must remain available for the next native occurrence.
				legacy := c.Runtime.Responses[0]
				legacy.Submission = nil
				legacy.Actor = &inputop.Actor{ID: "legacy-human", Kind: "human"}
				c.Runtime.Responses = append(c.Runtime.Responses, legacy)
				result := runResponsePersistence(t, c, target)
				require.Equal(t, "passed", result.Status, "%s", result.FailureReason)
				require.True(t, result.Runtime.Controls.Scenario.Complete)
				require.Len(t, result.Runtime.Submissions, controlledCount)
				first := result.Runtime.Submissions[0]
				require.True(t, first.Attempted)
				require.True(t, first.Complete)
				if stage != "healthy" {
					require.EqualValues(t, 1, first.Fault.Hits)
					require.True(t, first.Fault.Removed)
					require.Nil(t, first.ReturnedReceipt)
					require.Equal(t, "absent", first.Durable.Status)
					require.Zero(t, first.Checkpoints[0].State.Dispatches)
				}
				require.Len(t, result.Runtime.Reviews, 2)
				controlled, later := result.Runtime.Reviews[0], result.Runtime.Reviews[1]
				require.Equal(t, first.Pending.RequestID, controlled.RequestID)
				require.NotEqual(t, first.Pending.RequestID, later.RequestID)
				require.Equal(t, first.Pending.JobID, later.JobID)
				require.Equal(t, path, later.NodePath)
				require.Equal(t, fmt.Sprint(controlledCount), later.Receipt.SubmissionID)
				require.Equal(t, "legacy-human", later.Receipt.Actor.ID)
			})
		}
		t.Run(fmt.Sprintf("%t/legacy-before-controlled", structured), func(t *testing.T) {
			c, target := repeatedResponsePersistenceCase(structured)
			c.Runtime.Responses[0].Submission = nil
			c.Runtime.Responses[0].Actor = &inputop.Actor{ID: "legacy-human", Kind: "human"}
			s := c.Runtime.Controls.Scenario
			// Only the second occurrence is controlled. A future selector must
			// not reserve the entire job and block the earlier legacy reply.
			s.Events = s.Events[2:]
			s.Actions = s.Actions[2:]
			result := runResponsePersistence(t, c, target)
			require.Equal(t, "passed", result.Status, "%s", result.FailureReason)
			require.Len(t, result.Runtime.Submissions, 1)
			attempt := result.Runtime.Submissions[0]
			require.True(t, attempt.Complete)
			require.Equal(t, 2, attempt.Pending.Task.Occurrence)
			require.Len(t, result.Runtime.Reviews, 2)
			require.Equal(t, "legacy-human", result.Runtime.Reviews[0].Receipt.Actor.ID)
			require.Equal(t, "0", result.Runtime.Reviews[0].Receipt.SubmissionID)
			require.NotEqual(t, result.Runtime.Reviews[0].RequestID, attempt.Pending.RequestID)
			require.Equal(t, attempt.Durable.Receipt, result.Runtime.Reviews[1].Receipt)
		})
	}
}

func TestResponsePersistenceScopeOnlyAfterLegacyWait(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	for _, structured := range []bool{false, true} {
		t.Run(fmt.Sprintf("structured=%t", structured), func(t *testing.T) {
			fixture, target := responsePersistenceCase(structured, "healthy")
			begin := strings.Index(target.Content, "- id: reply\n")
			end := strings.Index(target.Content, "- id: continuation\n")
			require.GreaterOrEqual(t, begin, 0)
			require.Greater(t, end, begin)
			firstInput := strings.Replace(target.Content[begin:end], "- id: reply\n", "- id: first\n", 1)
			target.Content = target.Content[:begin] + firstInput + target.Content[begin:]

			const replyScope = `/scope:"$job"#1/attempt:1/scope:"sequence:persistence"#1/attempt:1/scope:"op:reply"#1`
			controlled := fixture.Runtime.Responses[0]
			controlled.Submission.Pending.Task.ScopeID = replyScope
			controlled.Submission.Pending.Task.NodePath = ""
			legacy := controlled
			legacy.NodePath = "persistence/first"
			legacy.Submission = nil
			legacy.Actor = &inputop.Actor{ID: "legacy-human", Kind: "human"}
			fixture.Runtime.Responses = []InputFixture{controlled, legacy}

			result := runResponsePersistence(t, fixture, target)
			require.Equal(t, "passed", result.Status, "%s", result.FailureReason)
			require.Len(t, result.Runtime.Reviews, 2)
			first, reply := result.Runtime.Reviews[0], result.Runtime.Reviews[1]
			require.Equal(t, "persistence/first", first.NodePath)
			require.Equal(t, "legacy-human", first.Receipt.Actor.ID)
			require.Equal(t, "persistence/reply", reply.NodePath)
			require.NotEqual(t, first.RequestID, reply.RequestID)
			require.Len(t, result.Runtime.Submissions, 1)
			attempt := result.Runtime.Submissions[0]
			require.True(t, attempt.Attempted)
			require.True(t, attempt.Complete)
			require.Equal(t, replyScope, attempt.Pending.Task.ScopeID)
			require.Equal(t, reply.RequestID, attempt.Pending.RequestID)
			require.Equal(t, reply.Receipt, attempt.Durable.Receipt)
		})
	}
}

func TestResponseSubmissionStaleOrMissingPendingTarget(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	for _, mode := range []string{"stale", "missing"} {
		t.Run(mode, func(t *testing.T) {
			c, target := repeatedResponsePersistenceCase(false)
			q := &c.Runtime.Responses[1].Submission.Pending.Task
			if mode == "stale" {
				q.Occurrence = 1
			} else {
				q.InvocationID = "unavailable-native-invocation"
			}
			result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), FixtureRoot: t.TempDir(), WorkRoot: t.TempDir()}, "reject-target", target, c, ExecutionOptions{Timeout: "20s"})
			require.NotEqual(t, "passed", result.Status)
			require.Len(t, result.Runtime.Submissions, 1, "never capture or answer a replacement occurrence")
			require.True(t, result.Runtime.Submissions[0].Attempted)
			require.NotEqual(t, "CANCELLED", result.Runtime.Controls.Final.Status)
			for _, call := range result.Runtime.Calls {
				require.NotEqual(t, "persistence/continuation", call.NodePath)
			}
			if mode == "stale" {
				require.Contains(t, result.FailureReason, "no longer active")
			}
		})
	}
}

func TestResponseSubmissionMatchedErrorDoesNotMaskRecoveryFailure(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	c, target := responsePersistenceCase(false, "response_output")
	s := c.Runtime.Controls.Scenario
	for i, action := range s.Actions {
		if action.ID == "start" {
			// Run before stop-again, regardless of how quickly replay suspends.
			tail := append([]ScenarioAction(nil), s.Actions[i+1:]...)
			s.Actions = append(s.Actions[:i+1], ScenarioAction{ID: "invalid-reopen", Do: "reopen_runtime", AfterAll: []string{"start"}})
			s.Actions = append(s.Actions, tail...)
			break
		}
	}
	result := runResponsePersistence(t, c, target)
	require.Equal(t, "failed", result.Status)
	require.True(t, result.Runtime.Submissions[0].Matched)
	require.True(t, result.Runtime.Submissions[0].Complete)
	require.Contains(t, result.FailureReason, "cannot reopen runtime with running workers")
	require.False(t, result.Runtime.Controls.Scenario.Complete)
}
