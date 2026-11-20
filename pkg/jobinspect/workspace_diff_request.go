package jobinspect

import (
	"bytes"
	"encoding/json"
	"io"
)

func (q *Request) UnmarshalJSON(raw []byte) error {
	type plain Request
	var selector struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(raw, &selector); err != nil {
		return err
	}
	if selector.Operation == "workspace_diff" {
		if _, err := strictWorkspaceObject(raw, map[string]bool{"operation": true, "job_id": true, "workspace_diff": true}); err != nil {
			return WorkspaceDiffDiagnostic("invalid_request", err.Error(), nil)
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode((*plain)(q)); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return WorkspaceDiffDiagnostic("invalid_request", "trailing inspection JSON", nil)
	}
	if q.Operation == "workspace_diff" {
		return validateWorkspaceInspection(*q)
	}
	return nil
}
