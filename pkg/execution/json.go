package execution

import (
	"bytes"
	"encoding/json"
)

// Keep an explicitly empty declaration across pinned recipe/invocation encoding.
func (r Requirements) MarshalJSON() ([]byte, error) {
	type plain Requirements
	if !r.CapabilitiesDeclared || len(r.Capabilities) > 0 {
		return json.Marshal(plain(r))
	}
	return json.Marshal(struct {
		plain
		Capabilities []string `json:"capabilities"`
	}{plain: plain(r), Capabilities: []string{}})
}
func (r *Requirements) UnmarshalJSON(raw []byte) error {
	if err := ValidatePythonField(raw); err != nil {
		return err
	}
	type plain Requirements
	var v plain
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	v.CapabilitiesDeclared = v.Capabilities != nil && len(v.Capabilities) == 0
	if len(v.Capabilities) == 0 {
		v.Capabilities = nil
	}
	*r = Requirements(v)
	return nil
}
