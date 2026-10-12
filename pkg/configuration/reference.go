package configuration

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const WireKey = "$c2j_ref"

type Reference struct {
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	ExpectedType string `json:"type,omitempty"`
}

func Ref(name string) Reference { return Reference{Kind: "config", Name: name} }
func (r Reference) MarshalJSON() ([]byte, error) {
	type plain Reference
	return json.Marshal(map[string]any{WireKey: plain(r)})
}
func (r Reference) MarshalYAML() (any, error) {
	raw, err := r.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var value any
	err = json.Unmarshal(raw, &value)
	return value, err
}
func Parse(value any) (Reference, bool, error) {
	var r Reference
	switch v := value.(type) {
	case Reference:
		r = v
	case map[string]any:
		raw, ok := v[WireKey]
		if !ok {
			return r, false, nil
		}
		fields, ok := raw.(map[string]any)
		if !ok || len(v) != 1 || (len(fields) != 2 && len(fields) != 3) {
			return r, true, fmt.Errorf("invalid config reference encoding")
		}
		r.Kind, _ = fields["kind"].(string)
		r.Name, _ = fields["name"].(string)
		if len(fields) == 3 {
			var ok bool
			r.ExpectedType, ok = fields["type"].(string)
			if !ok {
				return r, true, fmt.Errorf("invalid config reference type")
			}
		}
	default:
		return r, false, nil
	}
	if r.ExpectedType != "" && r.ExpectedType != "string" && r.ExpectedType != "number" && r.ExpectedType != "boolean" {
		return r, true, fmt.Errorf("unsupported config reference type")
	}
	if r.Kind != "config" || strings.TrimSpace(r.Name) == "" || len(r.Name) > 256 {
		return r, true, fmt.Errorf("invalid config reference")
	}
	return r, true, nil
}

var dotExpression = regexp.MustCompile(`^context\.config\.([A-Za-z_][A-Za-z0-9_-]*)$`)

func Expression(expr string) (Reference, bool) {
	if m := dotExpression.FindStringSubmatch(expr); len(m) == 2 {
		return Ref(m[1]), true
	}
	if strings.HasPrefix(expr, "context.config[") && strings.HasSuffix(expr, "]") {
		literal := strings.TrimSpace(expr[len("context.config[") : len(expr)-1])
		var name string
		if json.Unmarshal([]byte(literal), &name) == nil {
			return Ref(name), true
		}
	}
	return Reference{}, false
}
func Template(text string) (Reference, bool) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "${{") && strings.HasSuffix(text, "}}") {
		return Expression(strings.TrimSpace(text[3 : len(text)-2]))
	}
	return Reference{}, false
}
func Transform(value any, resolve func(Reference) (any, error)) (any, error) {
	if r, ok, err := Parse(value); ok || err != nil {
		if err != nil {
			return nil, err
		}
		return resolve(r)
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			n, err := Transform(item, resolve)
			if err != nil {
				return nil, err
			}
			out[k] = n
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			n, err := Transform(item, resolve)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	default:
		return value, nil
	}
}
func Contains(value any) bool {
	found := false
	_, err := Transform(value, func(r Reference) (any, error) { found = true; return r, nil })
	return found || err != nil
}
