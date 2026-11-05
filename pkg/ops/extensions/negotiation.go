package extensions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/colony-2/c2j/pkg/execution"
)

const executionRequiredExit = 75

// Decode a separate control contract: never run the success output schema.
func decodeExecutionRequired(raw []byte) (execution.Requirements, error) {
	// Reject duplicate fields and nulls as well as unknown fields. Silently
	// ignoring either could change the requested scheduling constraints.
	dec := json.NewDecoder(bytes.NewReader(raw))
	var check func() error
	check = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		if token == nil {
			return fmt.Errorf("null is not a requirement")
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for dec.More() {
					key, err := dec.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return fmt.Errorf("duplicate or invalid field")
					}
					seen[name] = true
					if err := check(); err != nil {
						return err
					}
				}
			case '[':
				for dec.More() {
					if err := check(); err != nil {
						return err
					}
				}
			default:
				return fmt.Errorf("unexpected delimiter")
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	if err := check(); err != nil {
		return execution.Requirements{}, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return execution.Requirements{}, fmt.Errorf("expected exactly one JSON object")
	}
	// A dedicated wire shape excludes all legacy platform/image properties.
	var envelope struct {
		Required *struct {
			Base         *string             `json:"base,omitempty"`
			Resources    execution.Resources `json:"resources,omitempty"`
			Capabilities []string            `json:"capabilities,omitempty"`
		} `json:"execution_required"`
	}
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&envelope); err != nil {
		return execution.Requirements{}, err
	}
	if envelope.Required == nil {
		return execution.Requirements{}, fmt.Errorf("execution_required is required")
	}
	v := envelope.Required
	r, err := (execution.Requirements{Base: v.Base, Resources: v.Resources, Capabilities: v.Capabilities}).Normalize()
	if err != nil {
		return execution.Requirements{}, err
	}
	if r.Empty() {
		return execution.Requirements{}, fmt.Errorf("execution_required must be nonempty")
	}
	return r, nil
}
