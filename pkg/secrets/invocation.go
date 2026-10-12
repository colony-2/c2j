package secrets

import (
	"reflect"
	"sort"
	"strings"
)

// Forwarding fields carry references to a child job and never materialize them
// in the parent. The child validates its own recipe schema before executing.
func InvocationNames(t reflect.Type, input any) ([]string, error) {
	set := map[string]bool{}
	_, err := transformInput(t, input, "", false, func(r Reference) (any, error) { set[r.Name] = true; return r, nil })
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, err
}
func MaterializeInput(s *Session, t reflect.Type, input any) (any, error) {
	return transformInput(t, input, "", true, func(r Reference) (any, error) { return s.Materialize(r) })
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
func transformInput(t reflect.Type, v any, mode string, preserveSchema bool, f func(Reference) (any, error)) (any, error) {
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
			value, err := transformInput(child, v, childMode, preserveSchema, f)
			if err != nil {
				return nil, err
			}
			out[k] = value
		}
		return out, nil
	case []any:
		var child reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			child = t.Elem()
		}
		out := make([]any, len(x))
		for i, v := range x {
			n, err := transformInput(child, v, "", preserveSchema, f)
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
