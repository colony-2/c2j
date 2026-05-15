package template

import (
	"fmt"
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

	next := cloneTemplateVars(rc.TemplateData.Vars)
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
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = cloneTemplateVarValue(value)
	}
	return out
}

func cloneTemplateVarValue(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		cloned := make(map[string]interface{}, len(v))
		for key, item := range v {
			cloned[key] = cloneTemplateVarValue(item)
		}
		return cloned
	case []interface{}:
		cloned := make([]interface{}, len(v))
		for i, item := range v {
			cloned[i] = cloneTemplateVarValue(item)
		}
		return cloned
	default:
		return value
	}
}
