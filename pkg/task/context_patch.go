package task

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ContextPatch is an ordered RFC 6902 operation array. Paths use RFC 6901
// pointers into context, sequence, or states. A supplied empty patch encodes as [].
type ContextPatch []PatchOperation

// PatchOperation preserves exact JSON values, including explicit null.
// Extension members are ignored as required by RFC 6902.
type PatchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	From  string          `json:"from,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

func (p ContextPatch) MarshalJSON() ([]byte, error) {
	if p == nil {
		return []byte("[]"), nil
	}
	type plain ContextPatch
	return json.Marshal(plain(p))
}
func (p *ContextPatch) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return fmt.Errorf("context patch must be an RFC 6902 operation array; migrate legacy merge objects to operations")
	}
	var ops []json.RawMessage
	if err := json.Unmarshal(raw, &ops); err != nil {
		return err
	}
	out := make(ContextPatch, 0, len(ops))
	for i, rawOp := range ops {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawOp, &fields); err != nil || fields == nil {
			return fmt.Errorf("operation %d must be an object", i)
		}
		var op PatchOperation
		for key, dst := range map[string]*string{"op": &op.Op, "path": &op.Path} {
			v, ok := fields[key]
			if !ok || bytes.Equal(v, []byte("null")) || json.Unmarshal(v, dst) != nil {
				return fmt.Errorf("operation %d requires string %s", i, key)
			}
		}
		switch op.Op {
		case "add", "replace", "test":
			v, ok := fields["value"]
			if !ok {
				return fmt.Errorf("operation %d requires value (null is allowed)", i)
			}
			op.Value = v
		case "move", "copy":
			v, ok := fields["from"]
			if !ok || bytes.Equal(v, []byte("null")) || json.Unmarshal(v, &op.From) != nil {
				return fmt.Errorf("operation %d requires string from", i)
			}
		case "remove":
		default:
			return fmt.Errorf("operation %d has unsupported op", i)
		}
		out = append(out, op)
	}
	*p = out
	return nil
}
