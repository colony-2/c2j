// Package secrets carries opaque references across durable boundaries. Only a
// live runner session may replace them with plaintext.
package secrets

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const WireKey = "$c2j_secret"

type Reference struct {
	Name string `json:"$c2j_secret" yaml:"$c2j_secret"`
}

var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,255}$`)
var expressionPattern = regexp.MustCompile(`^context\.secrets\.([A-Za-z_][A-Za-z0-9_-]{0,255})$`)
var templatePattern = regexp.MustCompile(`\$?\{\{\s*context\.secrets\.([A-Za-z_][A-Za-z0-9_-]{0,255})\s*\}\}`)

func Template(text string) (Reference, bool) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "${{") && strings.HasSuffix(text, "}}") {
		return Expression(strings.TrimSpace(text[3 : len(text)-2]))
	}
	return Reference{}, false
}

func Expression(expr string) (Reference, bool) {
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
		if len(x) != 1 {
			return r, true, fmt.Errorf("invalid secret reference")
		}
		r.Name, _ = n.(string)
	default:
		return r, false, nil
	}
	if !namePattern.MatchString(r.Name) {
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
			out[k] = n
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			n, err := Transform(v, f)
			if err != nil {
				return nil, err
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
	_, err := Transform(v, func(r Reference) (any, error) { set[r.Name] = true; return r, nil })
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, err
}

func Contains(v any) bool { n, e := Names(v); return e != nil || len(n) > 0 }

// Candidates only scans data already loaded by the compiler. It performs no
// expression evaluation, include resolution, or extension retrieval.
func Candidates(v any) []string {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, m := range templatePattern.FindAllSubmatch(b, -1) {
		set[string(m[1])] = true
	}
	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil {
		return nil
	}
	n, _ := Names(decoded)
	for _, s := range n {
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Placeholder is for type validation only and is never used for execution.
func Placeholder(v any) (any, error) {
	return Transform(v, func(Reference) (any, error) { return "secret-reference", nil })
}
