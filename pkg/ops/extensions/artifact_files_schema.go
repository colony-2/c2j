package extensions

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const artifactFileAnnotation = "x-c2j-artifact-file"

// These branches constrain the same instance as their parent. Child properties,
// array items, definitions and example/default data describe other values and
// must not be mistaken for conflicting handlers on the file reference itself.
func walkArtifactFileInstance(s map[string]any, path string, visit func(map[string]any, string) error) error {
	if err := visit(s, path); err != nil {
		return err
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "not", "if", "then", "else", "dependentSchemas", "dependencies"} {
		check := func(child any, suffix string) error {
			if schema, ok := child.(map[string]any); ok {
				return walkArtifactFileInstance(schema, path+"."+keyword+suffix, visit)
			}
			return nil
		}
		switch value := s[keyword].(type) {
		case []any:
			for i, child := range value {
				if err := check(child, fmt.Sprintf("[%d]", i)); err != nil {
					return err
				}
			}
		case map[string]any:
			if keyword == "dependentSchemas" || keyword == "dependencies" {
				for name, child := range value {
					if err := check(child, "."+name); err != nil {
						return err
					}
				}
			} else if err := check(value, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkArtifactFileHandler(s map[string]any, path string) error {
	if s["x-secret"] == true || s["x-c2j-object-type"] != nil || s["$ref"] != nil || s["$dynamicRef"] != nil {
		return fmt.Errorf("%s: artifact file property conflicts with secret, checkpoint or reference schema handling", path)
	}
	return nil
}

func validateArtifactFileHandlers(s map[string]any, path string) error {
	return walkArtifactFileInstance(s, path, checkArtifactFileHandler)
}

// Enclosing compositions can declare the same input property again. Inspect
// every applicable declaration before generic handler expansion can turn the
// reference into a secret/checkpoint schema. Reference indirection on the
// enclosing instance is unsupported, just as it is on the file property.
func validateArtifactFileDeclarations(input map[string]any, name string) error {
	return walkArtifactFileInstance(input, "input_schema", func(s map[string]any, path string) error {
		if err := checkArtifactFileHandler(s, path); err != nil {
			return err
		}
		declared := false
		if properties, ok := s["properties"].(map[string]any); ok {
			property, present := properties[name]
			declared = present
			if schema, ok := property.(map[string]any); ok {
				if err := validateArtifactFileHandlers(schema, path+".properties."+name); err != nil {
					return err
				}
			}
		}
		if patterns, ok := s["patternProperties"].(map[string]any); ok {
			for pattern, property := range patterns {
				matches, err := regexp.MatchString(pattern, name)
				if err != nil {
					return fmt.Errorf("%s.patternProperties.%s: %w", path, pattern, err)
				}
				if !matches {
					continue
				}
				declared = true
				if schema, ok := property.(map[string]any); ok {
					if err := validateArtifactFileHandlers(schema, path+".patternProperties."+pattern); err != nil {
						return err
					}
				}
			}
		}
		if !declared {
			for _, keyword := range []string{"additionalProperties", "unevaluatedProperties"} {
				if schema, ok := s[keyword].(map[string]any); ok {
					if err := validateArtifactFileHandlers(schema, path+"."+keyword); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

// Walk schema-valued keywords only. In particular defaults and examples are data,
// and a property whose name happens to match the annotation is still a property.
func walkFileSchema(s map[string]any, path string, root, input bool, names *[]string) error {
	if value, exists := s[artifactFileAnnotation]; exists {
		if value != true {
			return fmt.Errorf("%s.%s must be boolean true", path, artifactFileAnnotation)
		}
		if !input || root || names == nil {
			return fmt.Errorf("%s: %s is supported only on direct input_schema.properties", path, artifactFileAnnotation)
		}
		if s["type"] != "object" {
			return fmt.Errorf("%s: artifact file property requires type: object", path)
		}
		if err := validateArtifactFileHandlers(s, path); err != nil {
			return err
		}
	}
	for _, keyword := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies"} {
		if fields, ok := s[keyword].(map[string]any); ok {
			for name, value := range fields {
				if child, ok := value.(map[string]any); ok {
					var direct *[]string
					if root && input && keyword == "properties" {
						direct = names
						if _, exists := child[artifactFileAnnotation]; exists {
							*names = append(*names, name)
						}
					}
					if err := walkFileSchema(child, path+"."+keyword+"."+name, false, input, direct); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, keyword := range []string{"items", "additionalItems", "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "contains", "propertyNames", "not", "if", "then", "else", "contentSchema", "allOf", "anyOf", "oneOf", "prefixItems"} {
		switch value := s[keyword].(type) {
		case map[string]any:
			if err := walkFileSchema(value, path+"."+keyword, false, input, nil); err != nil {
				return err
			}
		case []any:
			for i, item := range value {
				if child, ok := item.(map[string]any); ok {
					if err := walkFileSchema(child, fmt.Sprintf("%s.%s[%d]", path, keyword, i), false, input, nil); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func artifactFileProperties(input, output map[string]any) ([]string, error) {
	names := []string{}
	if err := walkFileSchema(input, "input_schema", true, true, &names); err != nil {
		return nil, err
	}
	if err := walkFileSchema(output, "output_schema", true, false, nil); err != nil {
		return nil, err
	}
	sort.Strings(names)
	for _, name := range names {
		if err := validateArtifactFileDeclarations(input, name); err != nil {
			return nil, fmt.Errorf("input_schema.properties.%s: %w", name, err)
		}
	}
	return names, nil
}

// ArtifactFileKeys validates supplied annotated values, including JSON maps
// restored from compact transport, and returns their full stored keys.
func (r *ResolvedOp) ArtifactFileKeys(inputs map[string]any) ([]jobdb.ArtifactKey, error) {
	var keys []jobdb.ArtifactKey
	for _, name := range r.artifactFiles {
		value, exists := inputs[name]
		if !exists {
			continue // ordinary schema required/default rules decide omission
		}
		ref, err := artifacts.ParseFileRef(value)
		if err != nil {
			return nil, fmt.Errorf("inputs.%s: %w", name, err)
		}
		if key, ok := ref.StoredKey(); ok {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func artifactFileConstraint() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string", "minLength": 1} }
	object := func(required []string, properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "required": required, "properties": properties, "additionalProperties": false}
	}
	key := object([]string{"jobId", "taskOrdinal", "name", "sizeBytes"}, map[string]any{
		"jobId": str(), "name": str(),
		"taskOrdinal": map[string]any{"type": "integer", "minimum": 0, "maximum": int64(1<<63 - 1)},
		"sizeBytes":   map[string]any{"type": "integer", "minimum": -1, "maximum": int64(1<<63 - 1)},
	})
	stored := object([]string{"kind", "stored"}, map[string]any{
		"kind": map[string]any{"const": "stored"}, "name": map[string]any{"type": "string"},
		"stored": object([]string{"key"}, map[string]any{"key": key}),
	})
	external := object([]string{"kind", "name", "external"}, map[string]any{
		"kind": map[string]any{"const": "external"}, "name": str(),
		"external": object([]string{"url"}, map[string]any{"url": str(), "expand": map[string]any{"const": false}}),
	})
	return map[string]any{"anyOf": []any{stored, external}}
}

// Keep durable manifests untouched; constraints are compiled into validators.
func expandArtifactFileSchema(schema, original map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		out[k] = v
	}
	if fields, ok := schema["properties"].(map[string]any); ok {
		next := make(map[string]any, len(fields))
		for name, value := range fields {
			next[name] = value
			if prop, ok := value.(map[string]any); ok && prop[artifactFileAnnotation] == true {
				copy := make(map[string]any, len(prop)+1)
				for k, v := range prop {
					copy[k] = v
				}
				// The generic secret schema expander uses legacy float64 decoding.
				// File-reference defaults must retain their exact integer key fields.
				if originals, ok := original["properties"].(map[string]any); ok {
					if source, ok := originals[name].(map[string]any); ok {
						if value, present := source["default"]; present {
							copy["default"] = value
						}
					}
				}
				all, _ := copy["allOf"].([]any)
				copy["allOf"] = append(append([]any{}, all...), artifactFileConstraint())
				next[name] = copy
			}
		}
		out["properties"] = next
	}
	return out
}
