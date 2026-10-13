package secrets

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Forwarding fields carry references to a child job and never materialize them
// in the parent. The child validates its own recipe schema before executing.
func InvocationNames(t reflect.Type, input any) ([]string, error) {
	set := map[string]bool{}
	_, err := transformInput(t, input, "", false, nil, func(r Reference) (any, error) {
		if r.Name != "" {
			set[r.Name] = true
		}
		return r, nil
	})
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, err
}
func MaterializeInput(s *Session, t reflect.Type, input any) (any, error) {
	value, _, err := MaterializeInputWithUnsetEnv(s, t, input)
	return value, err
}

func MaterializeInputWithUnsetEnv(s *Session, t reflect.Type, input any) (any, []string, error) {
	var unset []string
	value, err := transformInput(t, input, "", true, func(mode, key string) {
		if mode == "env" {
			unset = append(unset, key)
		}
	}, s.materializeReference)
	if _, omit := value.(omitted); omit {
		return nil, nil, fmt.Errorf("null secret fallback requires an object field")
	}
	return value, unset, err
}

func fieldForName(t reflect.Type, name string) (reflect.StructField, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return reflect.StructField{}, false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if f.Anonymous && tag == "" {
			if nested, ok := fieldForName(f.Type, name); ok {
				return nested, true
			}
		}
		if tag == "" {
			tag = f.Name
		}
		if tag == name {
			return f, true
		}
	}
	return reflect.StructField{}, false
}
func transformInput(t reflect.Type, v any, mode string, preserveSchema bool, onOmit func(string, string), f func(Reference) (any, error)) (any, error) {
	if mode == "forward" || (mode == "schema" && preserveSchema) {
		return Transform(v, func(r Reference) (any, error) { return r, nil })
	}
	if r, ok, err := Parse(v); ok || err != nil {
		if err != nil {
			return nil, err
		}
		return f(r)
	}
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			var child reflect.Type
			var childMode string
			if t != nil {
				switch t.Kind() {
				case reflect.Struct:
					if field, ok := fieldForName(t, k); ok {
						child = field.Type
						childMode = field.Tag.Get("secret")
					}
				case reflect.Map:
					child = t.Elem()
				}
			}
			value, err := transformInput(child, v, childMode, preserveSchema, onOmit, f)
			if err != nil {
				return nil, err
			}
			if _, omit := value.(omitted); omit {
				if onOmit != nil {
					onOmit(mode, k)
				}
			} else {
				out[k] = value
			}
		}
		return out, nil
	case []any:
		var child reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			child = t.Elem()
		}
		out := make([]any, len(x))
		for i, v := range x {
			n, err := transformInput(child, v, "", preserveSchema, onOmit, f)
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

// InvocationReferences excludes references forwarded to child jobs.
func InvocationReferences(t reflect.Type, input any) ([]Reference, error) {
	var refs []Reference
	_, err := transformInput(t, input, "", false, nil, func(r Reference) (any, error) { refs = append(refs, r); return r, nil })
	return refs, err
}
