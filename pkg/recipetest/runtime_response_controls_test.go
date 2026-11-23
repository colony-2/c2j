package recipetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	inputop "github.com/colony-2/c2j/pkg/input"
	"github.com/stretchr/testify/require"
)

func TestResponseSubmissionStrictDeclarations(t *testing.T) {
	const valid = `{"id":"attempt-a","pending":{"job":"root","task":{"task_type":"input:collect_user_input","node_path":"persistence/reply","attempt":2,"occurrence":3}},"after_all":["stop"],"fault":"claim","expect_error":{"kind":"storage","contains":"boundary"}}`
	var s ResponseSubmission
	require.NoError(t, json.Unmarshal([]byte(valid), &s))
	require.Equal(t, 2, s.Pending.Task.Attempt)
	require.Equal(t, 3, s.Pending.Task.Occurrence)
	for _, raw := range []string{
		"null", `[]`, `{"id":"a","id":"b"}`, `{"id":null}`, `{"pending":null}`, `{"fault":null}`, `{"fault":""}`, `{"after_all":null}`, `{"expect_error":null}`, `{"expect_error":{"kind":"storage","contains":" "}}`, `{"expect_error":{"kind":"storage","typo":true}}`, `{"pending":{"job":"root","task":{"attempt":1,"attempt":2}}}`, `{"pending":{"job":"root","task":{"typo":1}}}`, `{"ID":"a"}`, `{"Pending":{"job":"root"}}`, `{"pending":{"Job":"root"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var r InputFixture
			require.Error(t, json.Unmarshal([]byte(`{"node_path":"persistence/reply","submission":`+raw+`}`), &r))
		})
	}
	for _, raw := range []string{`{"submission":{},"submission":{}}`, `{"submission":null}`, `{"Submission":{}}`} {
		var r InputFixture
		require.Error(t, json.Unmarshal([]byte(raw), &r))
	}
	var r InputFixture
	require.NoError(t, json.Unmarshal([]byte(`{"node_path":"persistence/reply","actor":{"id":"me","kind":"human"},"fields":{"value":"unaltered"},"submission":`+valid+`}`), &r))
	require.Equal(t, "unaltered", r.Fields["value"])
	require.Equal(t, "me", r.Actor.ID)
}
func TestResponseSubmissionValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Case)
		want string
	}{
		{"memory", func(c *Case) { c.Runtime.Backend = "memory" }, "sqlite"},
		{"no scenario", func(c *Case) { c.Runtime.Controls = nil }, "scenario"},
		{"case masking", func(c *Case) { c.ExpectError = "storage" }, "expect_error"},
		{"runtime masking", func(c *Case) { c.Runtime.ExpectError = "storage" }, "expect_error"},
		{"blank", func(c *Case) { c.Runtime.Responses[0].Submission.ID = " " }, "blank"},
		{"duplicate", func(c *Case) { c.Runtime.Responses = append(c.Runtime.Responses, c.Runtime.Responses[0]) }, "duplicate"},
		{"unknown job", func(c *Case) { c.Runtime.Responses[0].Submission.Pending.Job = "absent" }, "unknown"},
		{"wrong task", func(c *Case) { c.Runtime.Responses[0].Submission.Pending.Task.TaskType = "input:generate_form" }, "collect_user_input"},
		{"zero attempt", func(c *Case) { c.Runtime.Responses[0].Submission.Pending.Task.Attempt = 0 }, "positive"},
		{"zero occurrence", func(c *Case) { c.Runtime.Responses[0].Submission.Pending.Task.Occurrence = 0 }, "positive occurrence"},
		{"node mismatch", func(c *Case) { c.Runtime.Responses[0].NodePath = "different" }, "disagree"},
		{"wildcard", func(c *Case) { c.Runtime.Responses[0].Submission.Pending.Task.NodePath = "*" }, "exact"},
		{"stage", func(c *Case) { c.Runtime.Responses[0].Submission.Fault = "output" }, "unsupported"},
		{"kind", func(c *Case) {
			c.Runtime.Responses[0].Submission.ExpectError = &ResponseExpectedError{Kind: "cancellation"}
		}, "unsupported"},
		{"gate", func(c *Case) { c.Runtime.Responses[0].Submission.AfterAll = []string{"unknown"} }, "prerequisite"},
		{"cycle", func(c *Case) { c.Runtime.Responses[0].Submission.AfterAll = []string{"attempted"} }, "cycle"},
		{"event ref", func(c *Case) { c.Runtime.Controls.Scenario.Events[0].Response = "unknown" }, "unknown response"},
		{"event identity", func(c *Case) { c.Runtime.Controls.Scenario.Events[0].Job = "other" }, "identity"},
		{"event state", func(c *Case) { c.Runtime.Controls.Scenario.Events[0].Phase = "recovered" }, "requires pending or completed"},
		{"expiry ref", func(c *Case) {
			c.Runtime.Controls.Scenario.Actions = append(c.Runtime.Controls.Scenario.Actions, ScenarioAction{ID: "bad-expiry", Do: "expire_response_lease", Target: "unknown", AfterAll: []string{"pending"}})
		}, "unknown response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := responsePersistenceCase(false, "healthy")
			tc.edit(&c)
			issues := validateRuntimeCase(HarnessOptions{}, c)
			require.NotEmpty(t, issues)
			raw, _ := json.Marshal(issues)
			require.Contains(t, string(raw), tc.want)
		})
	}
	c, _ := responsePersistenceCase(false, "claim")
	require.Empty(t, validateRuntimeCase(HarnessOptions{}, c))
}
func TestResponseSubmissionTypedMatching(t *testing.T) {
	for _, kind := range []string{"storage", "input_not_pending", "stale_request", "validation"} {
		base := map[string]error{"storage": inputop.ErrStorage, "input_not_pending": inputop.ErrInputNotPending, "stale_request": inputop.ErrStaleRequest, "validation": inputop.ErrValidation}[kind]
		err := fmt.Errorf("boundary: %w", base)
		require.True(t, matchesResponseError(&ResponseExpectedError{Kind: kind, Contains: "boundary"}, err))
		require.False(t, matchesResponseError(&ResponseExpectedError{Kind: kind, Contains: "missing"}, err))
		require.False(t, matchesResponseError(&ResponseExpectedError{Kind: kind}, errors.New(err.Error())))
		require.False(t, matchesResponseError(nil, err))
		require.False(t, matchesResponseError(&ResponseExpectedError{Kind: kind}, nil))
		for _, cancel := range []error{context.Canceled, context.DeadlineExceeded} {
			require.False(t, matchesResponseError(&ResponseExpectedError{Kind: kind}, errors.Join(base, cancel)))
		}
	}
	require.True(t, matchesResponseError(nil, nil))
}
func TestResponseSubmissionEvidenceBounds(t *testing.T) {
	c, _ := responsePersistenceCase(false, "healthy")
	f := &runtimeFixture{c: c}
	c.Runtime.Secrets = map[string]string{"TOKEN": "exact-private-fixture-token"}
	require.ErrorContains(t, f.requireExactResponseEvidence(map[string]string{"form": "exact-private-fixture-token"}), "redaction")
	c.Runtime.Secrets = nil
	c.Runtime.Controls.MaxEvidenceBytes = 32
	require.ErrorContains(t, f.requireExactResponseEvidence(strings.Repeat("x", 64)), "byte limit")
	require.NoError(t, f.requireExactResponseEvidence("small"))
}
