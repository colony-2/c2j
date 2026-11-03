package recipe

import (
	"fmt"
	"math"
	"reflect"
	"sort"

	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/secrets"
)

// InputContract is a version-independent, validated recipe input contract.
// It never mutates the authored schema, submitted values, or default values.
type InputContract struct {
	schema                        map[string]InputSchema
	allowUnspecifiedIncludeInputs bool
}

func inputKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (c InputContract) Apply(data map[string]interface{}) (map[string]interface{}, error) {
	return applyInputFields("", c.schema, nil, data)
}

// ApplyIncluded also preserves the legacy contract for includes with no declared
// inputs. The version adapter selects that policy, not the compiler.
func (c InputContract) ApplyIncluded(data map[string]interface{}) (map[string]interface{}, error) {
	if c.allowUnspecifiedIncludeInputs {
		return data, nil
	}
	return c.Apply(data)
}

func inputPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func applyInputFields(path string, fields map[string]InputSchema, extra *InputAdditionalProperties, data map[string]interface{}) (map[string]interface{}, error) {
	resolved := make(map[string]interface{}, len(data))
	for _, key := range inputKeys(fields) {
		def := fields[key]
		value, exists := data[key]
		fieldPath := inputPath(path, key)
		if !exists {
			if err := def.validateMissing(fieldPath); err != nil {
				return nil, err
			}
			if def.Default == nil {
				continue
			}
			value = def.Default
			if def.Secret {
				value = secrets.WithDefault(secrets.Reference{}, def.Default)
			}
		}
		normalized, err := applyInputValue(fieldPath, def, value)
		if err != nil {
			return nil, err
		}
		resolved[key] = normalized
	}
	for _, key := range inputKeys(data) {
		if _, declared := fields[key]; declared {
			continue
		}
		fieldPath := inputPath(path, key)
		if extra == nil || (!extra.Allowed && extra.Schema == nil) {
			return nil, fmt.Errorf("field '%s' is present in data but not defined in schema", fieldPath)
		}
		var value interface{}
		var err error
		if extra.Schema != nil {
			value, err = applyInputValue(fieldPath, *extra.Schema, data[key])
		} else {
			value, err = copyInputJSON(fieldPath, data[key])
		}
		if err != nil {
			return nil, err
		}
		resolved[key] = value
	}
	return resolved, nil
}

func applyInputValue(path string, def InputSchema, value interface{}) (interface{}, error) {
	if def.Secret {
		if text, ok := value.(string); ok {
			if ref, yes := secrets.Template(text); yes {
				value = ref
			}
		}
		if ref, ok, err := secrets.Parse(value); ok || err != nil {
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", path, err)
			}
			if ref.OmitIfUnavailable && def.Required {
				ref.Required = true
			}
			value = secrets.WithDefault(ref, def.Default)
		}
	} else {
		if text, ok := value.(string); ok {
			if ref, yes := configuration.Template(text); yes {
				value = ref
			}
		}
		if ref, ok, err := configuration.Parse(value); ok || err != nil {
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", path, err)
			}
			if def.Type != "string" && def.Type != "number" && def.Type != "boolean" {
				return nil, fmt.Errorf("config input %q requires a simple value schema", path)
			}
			if ref.ExpectedType != "" && ref.ExpectedType != def.Type {
				return nil, fmt.Errorf("conflicting config type for input %q", path)
			}
			ref.ExpectedType = def.Type
			value = ref
		}
	}
	// Structured children validate their own reference declarations.
	switch def.Type {
	case "object":
		m, ok := inputStringMap(value)
		if !ok {
			return nil, fmt.Errorf("field %q requires an object with string keys", path)
		}
		if _, checkpoint, err := objects.Parse(m); checkpoint || err != nil {
			return nil, fmt.Errorf("field %q requires ordinary data; declare checkpoint for an object checkpoint", path)
		}
		if secrets.Contains(m) && def.Properties == nil && def.AdditionalProperties == nil {
			return nil, fmt.Errorf("input %q is not marked secret", path)
		}
		extra := def.AdditionalProperties
		if extra == nil {
			extra = &InputAdditionalProperties{Allowed: true}
		}
		return applyInputFields(path, def.Properties, extra, m)
	case "array":
		rv := reflect.ValueOf(value)
		if !rv.IsValid() || (rv.Kind() != reflect.Array && rv.Kind() != reflect.Slice) || (rv.Kind() == reflect.Slice && rv.IsNil()) {
			return nil, fmt.Errorf("field %q requires an array", path)
		}
		out := make([]interface{}, rv.Len())
		for i := range out {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			var err error
			if def.Items == nil {
				out[i], err = copyInputJSON(itemPath, rv.Index(i).Interface())
			} else {
				out[i], err = applyInputValue(itemPath, *def.Items, rv.Index(i).Interface())
			}
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	default:
		if err := def.validate(path, value); err != nil {
			return nil, err
		}
		return value, nil
	}
}

func inputStringMap(value interface{}) (map[string]interface{}, bool) {
	rv := reflect.ValueOf(value)
	if !rv.IsValid() || rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String || rv.IsNil() {
		return nil, false
	}
	out := make(map[string]interface{}, rv.Len())
	iter := rv.MapRange()
	for iter.Next() {
		out[iter.Key().String()] = iter.Value().Interface()
	}
	return out, true
}

// Unconstrained JSON values cannot smuggle framework references past typed
// validation. References remain supported through explicitly declared fields.
func copyInputJSON(path string, value interface{}) (interface{}, error) {
	if secrets.Contains(value) || configuration.Contains(value) {
		return nil, fmt.Errorf("field %q requires a declared schema for secret or configuration references", path)
	}
	if _, ok, err := objects.Parse(value); ok || err != nil {
		return nil, fmt.Errorf("field %q requires a checkpoint schema for object references", path)
	}
	if value == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value, nil
	case reflect.Float32, reflect.Float64:
		if !math.IsNaN(rv.Float()) && !math.IsInf(rv.Float(), 0) {
			return value, nil
		}
	case reflect.Map:
		return applyInputValue(path, InputSchema{Type: "object"}, value)
	case reflect.Slice, reflect.Array:
		return applyInputValue(path, InputSchema{Type: "array"}, value)
	}
	return nil, fmt.Errorf("field %q requires a JSON value, got %T", path, value)
}

func validateInputDeclaration(path string, def InputSchema, depth int) error {
	if depth > 64 {
		return fmt.Errorf("input schema %q exceeds maximum nesting depth 64", path)
	}
	if err := def.ValidateSecretDeclaration(); err != nil {
		return fmt.Errorf("input %q: %w", path, err)
	}
	switch def.Type {
	case "string", "number", "boolean", "artifact", "artifact_map", "checkpoint", "object", "array":
	default:
		return fmt.Errorf("field '%s' has an unsupported schema type: %s", path, def.Type)
	}
	if def.ObjectType != "" && def.Type != "checkpoint" {
		return fmt.Errorf("input %q: object_type requires type checkpoint", path)
	}
	if def.Items != nil && def.Type != "array" {
		return fmt.Errorf("input %q: items requires type array", path)
	}
	if (def.Properties != nil || def.AdditionalProperties != nil) && def.Type != "object" {
		return fmt.Errorf("input %q: properties and additionalProperties require type object", path)
	}
	for _, key := range inputKeys(def.Properties) {
		if err := validateInputDeclaration(inputPath(path, key), def.Properties[key], depth+1); err != nil {
			return err
		}
	}
	if def.Items != nil {
		if err := validateInputDeclaration(path+"[]", *def.Items, depth+1); err != nil {
			return err
		}
	}
	if def.AdditionalProperties != nil && def.AdditionalProperties.Schema != nil {
		if err := validateInputDeclaration(path+".*", *def.AdditionalProperties.Schema, depth+1); err != nil {
			return err
		}
	}
	if def.Default != nil {
		value := def.Default
		if def.Secret {
			value = secrets.WithDefault(secrets.Reference{}, value)
		}
		if _, err := applyInputValue(path, def, value); err != nil {
			return fmt.Errorf("invalid default_value: %w", err)
		}
	}
	return nil
}

// ValidationDefaults supplies representative values only for dry-run template
// validation. Execution always uses Apply with actual submitted values.
func (c InputContract) ValidationDefaults() (map[string]interface{}, error) {
	out := make(map[string]interface{}, len(c.schema))
	for _, key := range inputKeys(c.schema) {
		value, err := inputValidationValue(key, c.schema[key])
		if err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, nil
}

func inputValidationValue(path string, def InputSchema) (interface{}, error) {
	if def.Secret {
		return secrets.WithDefault(secrets.Reference{Name: path}, def.Default), nil
	}
	if def.Default != nil {
		return applyInputValue(path, def, def.Default)
	}
	switch def.Type {
	case "checkpoint":
		typ := def.ObjectType
		if typ == "" {
			typ = "c2j.validation/v1"
		}
		return objects.ValidationRef(typ), nil
	case "string":
		return "", nil
	case "number":
		return 0, nil
	case "boolean":
		return false, nil
	case "object":
		out := make(map[string]interface{}, len(def.Properties))
		for _, key := range inputKeys(def.Properties) {
			value, err := inputValidationValue(inputPath(path, key), def.Properties[key])
			if err != nil {
				return nil, err
			}
			out[key] = value
		}
		return out, nil
	case "array":
		return []interface{}{}, nil
	default:
		return nil, fmt.Errorf("unsupported input type for %s: %s", path, def.typeName())
	}
}

// PrepareSubmittedSecrets rejects plaintext in declared secret fields before
// submission persists inputs. It does not apply defaults or rewrite other data.
func (c InputContract) PrepareSubmittedSecrets(data map[string]interface{}) (map[string]interface{}, error) {
	if data == nil {
		return nil, nil
	}
	out := make(map[string]interface{}, len(data))
	for _, key := range inputKeys(data) {
		value := data[key]
		if def, ok := c.schema[key]; ok {
			var err error
			value, err = prepareInputSecrets(key, def, value)
			if err != nil {
				return nil, err
			}
		}
		out[key] = value
	}
	return out, nil
}

func prepareInputSecrets(path string, def InputSchema, value interface{}) (interface{}, error) {
	if def.Secret {
		if text, ok := value.(string); ok {
			if ref, yes := secrets.Template(text); yes {
				value = ref
			}
		}
		if _, ok, err := secrets.Parse(value); err != nil || !ok {
			return nil, fmt.Errorf("secret input %q requires a reference, not plaintext", path)
		}
		return value, nil
	}
	if def.Type == "object" {
		m, ok := inputStringMap(value)
		if !ok {
			return value, nil // Shape validation happens at execution admission.
		}
		for _, key := range inputKeys(m) {
			child, declared := def.Properties[key]
			if !declared && def.AdditionalProperties != nil && def.AdditionalProperties.Schema != nil {
				child, declared = *def.AdditionalProperties.Schema, true
			}
			if declared {
				prepared, err := prepareInputSecrets(inputPath(path, key), child, m[key])
				if err != nil {
					return nil, err
				}
				m[key] = prepared
			}
		}
		return m, nil
	}
	if def.Type == "array" && def.Items != nil {
		rv := reflect.ValueOf(value)
		if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) || (rv.Kind() == reflect.Slice && rv.IsNil()) {
			return value, nil
		}
		out := make([]interface{}, rv.Len())
		for i := range out {
			prepared, err := prepareInputSecrets(fmt.Sprintf("%s[%d]", path, i), *def.Items, rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			out[i] = prepared
		}
		return out, nil
	}
	return value, nil
}
