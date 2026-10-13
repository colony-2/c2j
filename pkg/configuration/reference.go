package configuration

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/cel-go/common"
	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/parser"
)

const WireKey = "$c2j_ref"

type Reference struct {
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	ExpectedType string `json:"type,omitempty"`
	// Default is a JSON scalar used only when the name is absent, not for an existing null.
	Default json.RawMessage `json:"default,omitempty"`
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
		if !ok || len(v) != 1 || (len(fields) < 2 || len(fields) > 4) {
			return r, true, fmt.Errorf("invalid config reference encoding")
		}
		r.Kind, _ = fields["kind"].(string)
		r.Name, _ = fields["name"].(string)
		for key := range fields {
			switch key {
			case "kind", "name", "type", "default":
			default:
				return r, true, fmt.Errorf("invalid config reference encoding")
			}
		}
		if d, exists := fields["default"]; exists {
			var err error
			r.Default, err = json.Marshal(d)
			if err != nil {
				return r, true, fmt.Errorf("invalid config fallback: %w", err)
			}
		}
		if _, exists := fields["type"]; exists {
			var ok bool
			r.ExpectedType, ok = fields["type"].(string)
			if !ok {
				return r, true, fmt.Errorf("invalid config reference type")
			}
		}
	default:
		return r, false, nil
	}
	if len(r.Default) > 0 {
		var value any
		if err := json.Unmarshal(r.Default, &value); err != nil {
			return r, true, fmt.Errorf("invalid config fallback: %w", err)
		}
		switch value.(type) {
		case nil, string, bool, float64:
		default:
			return r, true, fmt.Errorf("config fallback must be a string, number, boolean or null")
		}
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

var fallbackExpression = regexp.MustCompile(`(?s)^context\.config\.\?([A-Za-z_][A-Za-z0-9_-]*)\.orValue\((.*)\)$`)
var indexedFallbackExpression = regexp.MustCompile(`(?s)^context\.config\[\?("(?:[^"\\]|\\.)*")\]\.orValue\((.*)\)$`)

func Expression(expr string) (Reference, bool) {
	m := fallbackExpression.FindStringSubmatch(expr)
	if m == nil {
		m = indexedFallbackExpression.FindStringSubmatch(expr)
		if m != nil {
			var name string
			if json.Unmarshal([]byte(m[1]), &name) != nil {
				return Reference{}, false
			}
			m[1] = name
		}
	}
	if len(m) == 3 {
		parsed, errs := parser.Parse(common.NewTextSource(strings.TrimSpace(m[2])))
		if len(errs.GetErrors()) != 0 || parsed.Expr().Kind() != ast.LiteralKind {
			return Reference{}, false
		}
		literal := parsed.Expr().AsLiteral()
		var value any
		switch literal.Type() {
		case types.StringType, types.BoolType, types.IntType, types.UintType, types.DoubleType:
			value = literal.Value()
		case types.NullType:
			value = nil
		default:
			return Reference{}, false
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return Reference{}, false
		}
		ref := Ref(m[1])
		ref.Default = raw
		return ref, true
	}
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
