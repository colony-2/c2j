package artifacts

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ParseValue recognizes only the explicit discriminator at an artifact boundary.
// RawMessage preserves null versus missing and keeps resolved content opaque.
func ParseValue(value any) (Ref, bool, error) {
	if r, ok := value.(Ref); ok && r.Kind == RefKindValue {
		return r, true, r.Validate()
	}
	m, ok := value.(map[string]any)
	if !ok || m["kind"] != string(RefKindValue) {
		return Ref{}, false, nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return Ref{}, true, fmt.Errorf("value artifact is not JSON encodable")
	}
	var r Ref
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return r, true, fmt.Errorf("invalid value artifact descriptor: %w", err)
	}
	return r, true, r.Validate()
}

// DecodeFromMap supports typed op inputs through the existing map decoder.
func (r *Ref) DecodeFromMap(value any) error {
	if parsed, ok, err := ParseValue(value); ok {
		*r = parsed
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, r)
}

// ValueBytes serializes a resolved value without evaluating document contents.
func (r Ref) ValueBytes() ([]byte, error) {
	if r.Kind != RefKindValue || r.Stored != nil || r.External != nil {
		return nil, fmt.Errorf("value artifact cannot contain stored/external payloads")
	}
	if len(r.Value) == 0 || !json.Valid(r.Value) {
		return nil, fmt.Errorf("value artifact requires a JSON-encodable value")
	}
	switch r.Format {
	case "text":
		var text *string
		if json.Unmarshal(r.Value, &text) != nil || text == nil {
			return nil, fmt.Errorf("text artifact value must be a string")
		}
		return []byte(*text), nil
	case "json":
		var out bytes.Buffer
		if err := json.Compact(&out, r.Value); err != nil {
			return nil, err
		}
		out.WriteByte('\n')
		return out.Bytes(), nil
	default:
		return nil, fmt.Errorf("value artifact format must be text or json")
	}
}
