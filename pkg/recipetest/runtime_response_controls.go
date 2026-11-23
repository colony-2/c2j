package recipetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	inputop "github.com/colony-2/c2j/pkg/input"
)

// ResponseSubmission controls exactly one native API call in a disposable case.
// It is deliberately separate from the production submission types.
type ResponseSubmission struct {
	ID          string                 `json:"id"`
	Pending     ResponsePending        `json:"pending"`
	AfterAll    []string               `json:"after_all,omitempty"`
	Fault       string                 `json:"fault,omitempty"`
	ExpectError *ResponseExpectedError `json:"expect_error,omitempty"`
}
type ResponsePending struct {
	Job  string        `json:"job"`
	Task TaskSelection `json:"task"`
}
type ResponseExpectedError struct {
	Kind     string `json:"kind"`
	Contains string `json:"contains,omitempty"`
}

// strictResponseJSON checks presence before encoding/json can overwrite a
// duplicate or turn an explicit null into an omitted optional control.
func strictResponseJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		if token == nil {
			return fmt.Errorf("submission members must not be null")
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name := key.(string)
				if name != strings.ToLower(name) {
					return fmt.Errorf("submission member must use lowercase: %s", name)
				}
				// No capitalization aliases in new controls.
				if seen[name] {
					return fmt.Errorf("duplicate submission member %q", name)
				}
				seen[name] = true
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("invalid submission delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing submission data")
	}
	return nil
}
func (s *ResponseSubmission) UnmarshalJSON(raw []byte) error {
	if err := strictResponseJSON(raw); err != nil {
		return err
	}
	type plain ResponseSubmission
	var v plain
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return fmt.Errorf("submission: %w", err)
	}
	// encoding/json accepts case aliases; the wire contract does not.
	var members map[string]json.RawMessage
	_ = json.Unmarshal(raw, &members)
	for key := range members {
		switch key {
		case "id", "pending", "after_all", "fault", "expect_error":
		default:
			return fmt.Errorf("unknown submission member %s", key)
		}
	}
	if raw, ok := members["fault"]; ok {
		var value string
		if json.Unmarshal(raw, &value) != nil || value == "" {
			return fmt.Errorf("fault requires a supported stage")
		}
	}
	if raw, ok := members["expect_error"]; ok {
		var m map[string]json.RawMessage
		_ = json.Unmarshal(raw, &m)
		if value, ok := m["contains"]; ok {
			var text string
			if json.Unmarshal(value, &text) != nil || strings.TrimSpace(text) == "" {
				return fmt.Errorf("expect_error.contains must be nonblank")
			}
		}
	}
	*s = ResponseSubmission(v)
	return nil
}
func strictSubmissionMember(raw []byte) error {
	// Preserve legacy response members, while detecting duplicate submission keys.
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return fmt.Errorf("response must be an object")
	}
	seen := false
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return err
		}
		if strings.EqualFold(key.(string), "submission") {
			if seen {
				return fmt.Errorf("duplicate submission member")
			}
			seen = true
			if key != "submission" {
				return fmt.Errorf("use submission in lowercase")
			}
			var s ResponseSubmission
			if err := json.Unmarshal(value, &s); err != nil {
				return err
			}
		}
	}
	return nil
}
func responseErrorKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "cancellation"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, inputop.ErrStorage):
		return "storage"
	case errors.Is(err, inputop.ErrInputNotPending):
		return "input_not_pending"
	case errors.Is(err, inputop.ErrStaleRequest):
		return "stale_request"
	case errors.Is(err, inputop.ErrValidation):
		return "validation"
	default:
		return "unknown"
	}
}
func matchesResponseError(want *ResponseExpectedError, err error) bool {
	if want == nil {
		return err == nil
	}
	kind := responseErrorKind(err)
	return err != nil && kind != "cancellation" && kind != "deadline" && kind == want.Kind && (want.Contains == "" || strings.Contains(err.Error(), want.Contains))
}
func responseSubmissionExists(c *RuntimeCase, id string) bool {
	for _, r := range c.Responses {
		if r.Submission != nil && r.Submission.ID == id {
			return true
		}
	}
	return false
}
func responseSpec(c *RuntimeCase, id string) *ResponseSubmission {
	for _, r := range c.Responses {
		if r.Submission != nil && r.Submission.ID == id {
			return r.Submission
		}
	}
	return nil
}
func validateResponseSubmissions(c Case) error {
	if c.Runtime == nil {
		return nil
	}
	ids := map[string]bool{}
	for _, r := range c.Runtime.Responses {
		s := r.Submission
		if s == nil {
			continue
		}
		if c.Runtime.Backend != "sqlite" || c.Runtime.Controls == nil || c.Runtime.Controls.Scenario == nil {
			return fmt.Errorf("response submission requires sqlite and scenario")
		}
		if c.ExpectError != "" || c.Runtime.ExpectError != "" {
			return fmt.Errorf("response submission cannot combine with case/runtime expect_error")
		}
		if strings.TrimSpace(s.ID) == "" || ids[s.ID] {
			return fmt.Errorf("blank or duplicate submission id %q", s.ID)
		}
		ids[s.ID] = true
		if _, ok := c.Runtime.Controls.Scenario.Jobs[s.Pending.Job]; !ok {
			return fmt.Errorf("unknown pending job %q", s.Pending.Job)
		}
		q := s.Pending.Task
		if err := validateTask(q); err != nil {
			return err
		}
		if q.TaskType != "input:collect_user_input" || q.Occurrence < 1 {
			return fmt.Errorf("pending requires input:collect_user_input and positive occurrence")
		}
		if q.NodePath != "" && q.NodePath != r.NodePath {
			return fmt.Errorf("pending and response node_path disagree")
		}
		switch s.Fault {
		case "", "claim", "response_output", "settlement":
		default:
			return fmt.Errorf("unsupported response fault %q", s.Fault)
		}
		if e := s.ExpectError; e != nil {
			switch e.Kind {
			case "storage", "input_not_pending", "stale_request", "validation":
			default:
				return fmt.Errorf("unsupported submission error kind %q", e.Kind)
			}
			if e.Contains != "" && strings.TrimSpace(e.Contains) == "" {
				return fmt.Errorf("expect_error.contains must be nonblank")
			}
		}
	}
	return nil
}
func validateResponseEvent(c *RuntimeCase, e ScenarioEvent) error {
	s := responseSpec(c, e.Response)
	if s == nil {
		return fmt.Errorf("unknown response %s", e.Response)
	}
	if e.Job != "" && e.Job != s.Pending.Job || e.Task != nil || e.Hold != "" {
		return fmt.Errorf("response event contradicts bound identity")
	}
	switch e.Phase {
	case "pending", "attempted":
		if e.Status != "" {
			return fmt.Errorf("response phase has no status")
		}
	case "recovered":
		if e.Status != "pending" && e.Status != "completed" {
			return fmt.Errorf("recovered response requires pending or completed")
		}
	default:
		return fmt.Errorf("unknown response phase %s", e.Phase)
	}
	return nil
}
func responseDependencyGraph(c *RuntimeCase, refs map[string]bool, graph map[string][]string) error {
	for _, r := range c.Responses {
		if s := r.Submission; s != nil {
			key := "response:" + s.ID
			if refs[key] {
				return fmt.Errorf("reserved response dependency name %s", key)
			}
			for _, p := range s.AfterAll {
				if !refs[p] {
					return fmt.Errorf("unknown response prerequisite %s", p)
				}
			}
			graph[key] = s.AfterAll
		}
	}
	for _, e := range c.Controls.Scenario.Events {
		if len(e.AfterAll) > 0 && e.Kind != "response" {
			return fmt.Errorf("event after_all requires response kind")
		}
		for _, p := range e.AfterAll {
			if !refs[p] {
				return fmt.Errorf("unknown event prerequisite %s", p)
			}
		}
		graph[e.ID] = append(graph[e.ID], e.AfterAll...)
		if e.Kind == "response" && e.Phase != "pending" {
			graph[e.ID] = append(graph[e.ID], "response:"+e.Response)
		}
	}
	return nil
}
