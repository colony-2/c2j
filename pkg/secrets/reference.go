// Package secrets carries opaque references across durable boundaries. Only a
// live runner session may replace them with plaintext.
package secrets

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/cel-go/common"
	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/parser"
)

const WireKey = "$c2j_secret"

// Reference holds an opaque name and an optional public fallback. An empty
// name with a fallback represents an omitted input with a schema default.
type Reference struct {
	Name              string  `json:"$c2j_secret" yaml:"$c2j_secret"`
	Default           *string `json:"default,omitempty" yaml:"default,omitempty"`
	OmitIfUnavailable bool    `json:"omit_if_unavailable,omitempty" yaml:"omit_if_unavailable,omitempty"`
	// Required preserves a recipe's constraint when a null fallback is forwarded.
	Required bool `json:"required,omitempty" yaml:"required,omitempty"`
}

var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,255}$`)
var expressionPattern = regexp.MustCompile(`^context\.secrets\.([A-Za-z_][A-Za-z0-9_-]{0,255})$`)
var fallbackPattern = regexp.MustCompile(`^context\.secrets\.\?([A-Za-z_][A-Za-z0-9_-]{0,255})\.orValue\((.*)\)$`)

func Template(text string) (Reference, bool) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "${{") && strings.HasSuffix(text, "}}") {
		return Expression(strings.TrimSpace(text[3 : len(text)-2]))
	}
	return Reference{}, false
}

func Expression(expr string) (Reference, bool) {
	if m := fallbackPattern.FindStringSubmatch(expr); len(m) == 3 {
		if strings.TrimSpace(m[2]) == "null" {
			return Reference{Name: m[1], OmitIfUnavailable: true}, true
		}
		parsed, errs := parser.Parse(common.NewTextSource(strings.TrimSpace(m[2])))
		if len(errs.GetErrors()) != 0 || parsed.Expr().Kind() != ast.LiteralKind {
			return Reference{}, false
		}
		fallback, ok := parsed.Expr().AsLiteral().Value().(string)
		if !ok {
			return Reference{}, false
		}
		return Reference{Name: m[1], Default: &fallback}, true
	}
	m := expressionPattern.FindStringSubmatch(expr)
	if len(m) != 2 {
		return Reference{}, false
	}
	return Reference{Name: m[1]}, true
}

func Parse(v any) (Reference, bool, error) {
	var r Reference
	switch x := v.(type) {
	case Reference:
		r = x
	case map[string]any:
		n, ok := x[WireKey]
		if !ok {
			return r, false, nil
		}
		for key := range x {
			switch key {
			case WireKey, "default", "omit_if_unavailable", "required":
			default:
				return r, true, fmt.Errorf("invalid secret reference")
			}
		}
		for key, dest := range map[string]*bool{"omit_if_unavailable": &r.OmitIfUnavailable, "required": &r.Required} {
			if value, exists := x[key]; exists {
				b, ok := value.(bool)
				if !ok {
					return r, true, fmt.Errorf("invalid secret reference")
				}
				*dest = b
			}
		}
		var valid bool
		r.Name, valid = n.(string)
		if !valid {
			return r, true, fmt.Errorf("invalid secret reference name")
		}
		if d, exists := x["default"]; exists {
			value, ok := d.(string)
			if !ok {
				return r, true, fmt.Errorf("secret default must be a string")
			}
			r.Default = &value
		}
	default:
		return r, false, nil
	}
	if (r.OmitIfUnavailable && r.Default != nil) || (r.Required && !r.OmitIfUnavailable) {
		return r, true, fmt.Errorf("invalid secret fallback")
	}
	if !namePattern.MatchString(r.Name) && !(r.Name == "" && r.Default != nil) {
		return r, true, fmt.Errorf("invalid secret reference name")
	}
	return r, true, nil
}

// Transform clones JSON-shaped data; a reserved reference object is atomic.
func Transform(v any, f func(Reference) (any, error)) (any, error) {
	if r, ok, err := Parse(v); ok || err != nil {
		if err != nil {
			return nil, err
		}
		return f(r)
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			n, err := Transform(v, f)
			if err != nil {
				return nil, err
			}
			if _, omit := n.(omitted); !omit {
				out[k] = n
			}
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			n, err := Transform(v, f)
			if err != nil {
				return nil, err
			}
			if _, omit := n.(omitted); omit {
				return nil, fmt.Errorf("null secret fallback cannot omit an array element")
			}
			out[i] = n
		}
		return out, nil
	default:
		return v, nil
	}
}

func Names(v any) ([]string, error) {
	set := map[string]bool{}
	_, err := Transform(v, func(r Reference) (any, error) {
		if r.Name != "" {
			set[r.Name] = true
		}
		return r, nil
	})
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, err
}

func Contains(v any) bool {
	found := false
	_, err := Transform(v, func(r Reference) (any, error) { found = true; return r, nil })
	return found || err != nil
}

// WithDefault attaches a public fallback without overriding an explicit fallback.
func WithDefault(r Reference, value any) Reference {
	if d, ok := value.(string); ok && r.Default == nil && !r.OmitIfUnavailable {
		r.Default = &d
	}
	return r
}

// ValidateDefault checks public literal defaults; they are never evaluated.
func ValidateDefault(value any) error {
	if _, ok := value.(string); !ok {
		return fmt.Errorf("secret default must be a string")
	}
	return nil
}

// Placeholder is for type validation only and is never used for execution.
func Placeholder(v any) (any, error) {
	return Transform(v, func(Reference) (any, error) { return "secret-reference", nil })
}

// omitted is transient and must never cross an op or durable boundary.
type omitted struct{}

func HasOmission(v any) bool {
	found := false
	_, _ = Transform(v, func(r Reference) (any, error) { found = found || r.OmitIfUnavailable; return r, nil })
	return found
}
