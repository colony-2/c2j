package template

import (
	"fmt"
	"reflect"
	"strings"
)

// ResolveVars evaluates a vars block against the current outer vars snapshot,
// then atomically overlays the rendered values onto the current scope.
func (rc *ResolutionContext) ResolveVars(vars map[string]interface{}) error {
	if len(vars) == 0 {
		return nil
	}
	rc.ensureContextBackfill()

	declared := make(map[string]struct{}, len(vars))
	for name := range vars {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("var name cannot be empty")
		}
		declared[name] = struct{}{}
	}
	for name, value := range vars {
		if referenced, ok := referencesDeclaredVar(value, declared); ok {
			return fmt.Errorf("var %q references %q from the same vars block", name, referenced)
		}
	}

	resolved := make(map[string]interface{}, len(vars))
	for name, value := range vars {
		resolvedValue, err := rc.resolveValue(value)
		if err != nil {
			return fmt.Errorf("failed to resolve var %q: %w", name, err)
		}
		resolved[name] = resolvedValue
	}

	// Build the new lexical binding once; inherited payloads are immutable.
	next := make(map[string]interface{}, len(rc.TemplateData.Vars)+len(resolved))
	for name, value := range rc.TemplateData.Vars {
		next[name] = value
	}
	for name, value := range resolved {
		next[name] = value
	}
	rc.TemplateData.Vars = next
	if rc.Options.DiagnosticsObserver != nil {
		rc.Options.DiagnosticsObserver.VarsResolved(rc.ScopeType, rc.TaskExecutionContext().Invocation.NodePath, cloneTemplateVars(rc.TemplateData.Vars))
	}
	return nil
}

func referencesDeclaredVar(value interface{}, declared map[string]struct{}) (string, bool) {
	switch v := value.(type) {
	case string:
		for name := range declared {
			if referencesVarName(v, name) {
				return name, true
			}
		}
	case map[string]interface{}:
		for _, item := range v {
			if name, ok := referencesDeclaredVar(item, declared); ok {
				return name, true
			}
		}
	case map[interface{}]interface{}:
		for _, item := range v {
			if name, ok := referencesDeclaredVar(item, declared); ok {
				return name, true
			}
		}
	case []interface{}:
		for _, item := range v {
			if name, ok := referencesDeclaredVar(item, declared); ok {
				return name, true
			}
		}
	}
	return "", false
}

func referencesVarName(expr string, name string) bool {
	if name == "" {
		return false
	}
	if containsVarDotReference(expr, name) {
		return true
	}
	doubleQuoted := `"` + name + `"`
	singleQuoted := `'` + name + `'`
	return strings.Contains(expr, "vars["+doubleQuoted+"]") ||
		strings.Contains(expr, "vars["+singleQuoted+"]") ||
		strings.Contains(expr, "vars[?"+doubleQuoted+"]") ||
		strings.Contains(expr, "vars[?"+singleQuoted+"]") ||
		strings.Contains(expr, "index vars "+doubleQuoted) ||
		strings.Contains(expr, "index vars "+singleQuoted)
}

func containsVarDotReference(expr string, name string) bool {
	needle := "vars." + name
	start := 0
	for {
		idx := strings.Index(expr[start:], needle)
		if idx < 0 {
			return false
		}
		pos := start + idx
		after := pos + len(needle)
		if after == len(expr) || !isIdentifierChar(expr[after]) {
			return true
		}
		start = after
	}
}

func isIdentifierChar(ch byte) bool {
	return ch == '_' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func cloneTemplateVars(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = cloneTemplateVarValue(value)
	}
	return out
}

func cloneTemplateVarValue(value interface{}) interface{} {
	if value == nil {
		return nil
	}
	return cloneRecipeContainer(reflect.ValueOf(value)).Interface()
}

// Recipe payloads are acyclic values. Copy containers by their native type so
// typed maps/lists and numeric leaves retain their CEL meaning.
func cloneRecipeContainer(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(cloneRecipeContainer(v.Elem()))
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		it := v.MapRange()
		for it.Next() {
			out.SetMapIndex(it.Key(), cloneRecipeContainer(it.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneRecipeContainer(v.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneRecipeContainer(v.Index(i)))
		}
		return out
	default:
		return v
	}
}
