package secrets

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// ExpandSchema admits a reference as the deferred representation of a secret
// string. ValidateSchema separately enforces that submission uses references.
func ExpandSchema(schema map[string]any) (map[string]any, error) {
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err = json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	var walk func(map[string]any) error
	walk = func(s map[string]any) error {
		if annotation, exists := s["x-secret"]; exists {
			if _, ok := annotation.(bool); !ok {
				return fmt.Errorf("x-secret must be a boolean")
			}
		}
		for _, key := range []string{"$defs", "definitions", "patternProperties", "allOf", "anyOf", "oneOf", "if", "then", "else", "not", "contains", "prefixItems", "dependentSchemas"} {
			if HasAnnotation(s[key]) {
				return fmt.Errorf("secret annotations under %s are unsupported; use properties, items, or additionalProperties", key)
			}
		}
		if yes, _ := s["x-secret"].(bool); yes {
			if err := validateSecretSchema(s); err != nil {
				return err
			}
			// Attach secret defaults to references via ApplySchemaDefaults;
			// the ordinary default engine must not insert plaintext.
			plain := map[string]any{}
			for k, v := range s {
				if k != "default" {
					plain[k] = v
				}
			}
			clear(s)
			s["anyOf"] = []any{plain, map[string]any{"type": "object", "required": []string{WireKey}, "additionalProperties": false, "properties": map[string]any{WireKey: map[string]any{"type": "string"}, "default": map[string]any{"type": "string"}, "omit_if_unavailable": map[string]any{"type": "boolean"}, "required": map[string]any{"type": "boolean"}}}}
			return nil
		}
		for _, key := range []string{"properties"} {
			if entries, ok := s[key].(map[string]any); ok {
				for _, v := range entries {
					if sub, ok := v.(map[string]any); ok {
						if err := walk(sub); err != nil {
							return err
						}
					}
				}
			}
		}
		for _, key := range []string{"items", "additionalProperties"} {
			if sub, ok := s[key].(map[string]any); ok {
				if err := walk(sub); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err = walk(out); err != nil {
		return nil, err
	}
	return out, nil
}

func HasAnnotation(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		if _, ok := v["x-secret"]; ok {
			return true
		}
		for _, child := range v {
			if HasAnnotation(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if HasAnnotation(child) {
				return true
			}
		}
	}
	return false
}

// ValidateType permits native ops to opt in with secret:"true" on string
// inputs, or secret:"env" on a map of environment variables. Extension payloads
// use secret:"schema" and are checked against their manifest before invocation.
func ValidateType(t reflect.Type, v any) error { return validateType(t, v, "") }
func validateType(t reflect.Type, v any, mode string) error {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if mode == "true" && (t == nil || t.Kind() != reflect.String) {
		return fmt.Errorf("secret input must have string type")
	}
	if mode == "schema" || mode == "forward" {
		_, err := Names(v)
		return err
	}
	if _, ok, err := Parse(v); ok || err != nil {
		if err != nil {
			return err
		}
		if mode == "true" || mode == "value" {
			return nil
		}
		return fmt.Errorf("secret reference requires a secret-enabled input")
	}
	if mode == "true" {
		return fmt.Errorf("secret input requires a reference, not plaintext")
	}
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return reject(v)
	}
	switch x := v.(type) {
	case map[string]any:
		if t.Kind() == reflect.Struct {
			for k, v := range x {
				field, found := fieldForName(t, k)
				if found {
					if err := validateType(field.Type, v, field.Tag.Get("secret")); err != nil {
						return fmt.Errorf("input %s: %w", k, err)
					}
				}
				if !found {
					if err := reject(v); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if t.Kind() == reflect.Map {
			for _, v := range x {
				m := ""
				if mode == "env" {
					m = "value"
				}
				if err := validateType(t.Elem(), v, m); err != nil {
					return err
				}
			}
			return nil
		}
	case []any:
		if t.Kind() == reflect.Slice {
			for _, v := range x {
				if err := validateType(t.Elem(), v, ""); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return reject(v)
}
func reject(v any) error {
	if Contains(v) {
		return fmt.Errorf("secret reference requires a secret-enabled input")
	}
	return nil
}

// Schema annotations apply to string leaves. Unsupported schema constructs
// cannot implicitly grant permission to a reference.
func ValidateSchema(schema map[string]any, v any, materialized bool) error {
	marked, _ := schema["x-secret"].(bool)
	if marked {
		if err := validateSecretSchema(schema); err != nil {
			return err
		}
		if materialized {
			if _, ok := v.(string); ok {
				return nil
			}
		}
		if _, ok, err := Parse(v); ok || err != nil {
			return err
		}
		return fmt.Errorf("secret input requires a reference, not plaintext")
	}
	if _, ok, err := Parse(v); ok || err != nil {
		return fmt.Errorf("secret reference requires x-secret: true")
	}
	switch x := v.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		for k, v := range x {
			s, _ := props[k].(map[string]any)
			if s == nil {
				s, _ = schema["additionalProperties"].(map[string]any)
			}
			if err := ValidateSchema(s, v, materialized); err != nil {
				return fmt.Errorf("input %s: %w", k, err)
			}
		}
	case []any:
		s, _ := schema["items"].(map[string]any)
		for _, v := range x {
			if err := ValidateSchema(s, v, materialized); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSecretSchema(schema map[string]any) error {
	if schema["type"] != "string" {
		return fmt.Errorf("secret schema requires type string")
	}
	if d, exists := schema["default"]; exists {
		return ValidateDefault(d)
	}
	return nil
}

// ApplySchemaDefaults attaches fallbacks to secret bindings, including omitted
// inputs. It never substitutes a named secret or evaluates default strings.
func ApplySchemaDefaults(schema map[string]any, value any) (any, error) {
	if marked, _ := schema["x-secret"].(bool); marked {
		if err := validateSecretSchema(schema); err != nil {
			return nil, err
		}
		if ref, ok, err := Parse(value); ok || err != nil {
			if err != nil {
				return nil, err
			}
			return WithDefault(ref, schema["default"]), nil
		}
		return value, nil
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		props, _ := schema["properties"].(map[string]any)
		for k, item := range v {
			child, _ := props[k].(map[string]any)
			if child == nil {
				child, _ = schema["additionalProperties"].(map[string]any)
			}
			resolved, err := ApplySchemaDefaults(child, item)
			if err != nil {
				return nil, err
			}
			out[k] = resolved
		}
		for k, child := range props {
			sub, _ := child.(map[string]any)
			if _, exists := out[k]; exists {
				continue
			}
			if marked, _ := sub["x-secret"].(bool); marked {
				if d, exists := sub["default"]; exists {
					if err := ValidateDefault(d); err != nil {
						return nil, err
					}
					out[k] = WithDefault(Reference{}, d)
				}
			}
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		sub, _ := schema["items"].(map[string]any)
		for i, item := range v {
			resolved, err := ApplySchemaDefaults(sub, item)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return value, nil
	}
}
