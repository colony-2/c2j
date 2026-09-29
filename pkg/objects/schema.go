package objects

import (
	"encoding/json"
	"fmt"
	"strings"
)

// JSONValue converts native references to the JSON values used by schema validators.
func JSONValue(value any) (any, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out any
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	err = d.Decode(&out)
	return out, err
}

// ValidationRef is a shape-only placeholder for recipe validation, never execution.
func ValidationRef(objectType string) map[string]any {
	r := Ref{Format: Version, Type: objectType, TenantID: "validation", SHA256: strings.Repeat("0", 64)}
	r.Artifact.JobId = "validation"
	r.Artifact.TaskOrdinal = 1
	r.Artifact.Name = ArtifactPrefix + r.SHA256 + ".tar"
	v, _ := JSONValue(r)
	return v.(map[string]any)
}

// ExpandSchema translates the object annotation into ordinary JSON Schema so
// nested properties, items, unions and local $refs use the same validator.
// Only schema-valued keywords are visited; defaults/examples remain data.
func ExpandSchema(schema map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		out[k] = v
	}
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
		if fields, ok := out[key].(map[string]any); ok {
			next := map[string]any{}
			for k, v := range fields {
				if sub, ok := v.(map[string]any); ok {
					n, err := ExpandSchema(sub)
					if err != nil {
						return nil, err
					}
					next[k] = n
				} else {
					next[k] = v
				}
			}
			out[key] = next
		}
	}
	for _, key := range []string{"items", "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "contains", "propertyNames", "not", "if", "then", "else"} {
		if sub, ok := out[key].(map[string]any); ok {
			n, err := ExpandSchema(sub)
			if err != nil {
				return nil, err
			}
			out[key] = n
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		if list, ok := out[key].([]any); ok {
			next := make([]any, len(list))
			for i, v := range list {
				next[i] = v
				if sub, ok := v.(map[string]any); ok {
					n, err := ExpandSchema(sub)
					if err != nil {
						return nil, err
					}
					next[i] = n
				}
			}
			out[key] = next
		}
	}
	if raw, ok := out["x-c2j-object-type"]; ok {
		typ, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("x-c2j-object-type must be a string")
		}
		if err := validateType(typ); err != nil {
			return nil, err
		}
		constraint := map[string]any{"type": "object", "required": []string{Marker, "type", "tenant_id", "artifact", "sha256"}, "properties": map[string]any{
			Marker: map[string]any{"const": Version}, "type": map[string]any{"const": typ},
			"tenant_id": map[string]any{"type": "string", "minLength": 1}, "sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"artifact": map[string]any{"type": "object", "required": []string{"jobId", "taskOrdinal", "name", "sizeBytes"}, "properties": map[string]any{
				"jobId": map[string]any{"type": "string", "minLength": 1}, "taskOrdinal": map[string]any{"type": "integer", "minimum": 1}, "name": map[string]any{"type": "string", "pattern": "^__c2j_objects__/[0-9a-f]{64}\\.tar$"}, "sizeBytes": map[string]any{"type": "integer", "minimum": 0},
			}},
		}}
		all, _ := out["allOf"].([]any)
		out["allOf"] = append(append([]any{}, all...), constraint)
	}
	return out, nil
}
