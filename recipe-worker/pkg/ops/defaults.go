package ops

import (
	"reflect"
	"strings"
)

// InjectDefaults adds default values to the input map for fields that are missing.
// Defaults are injected as strings and will be resolved by the template system.
// This function recursively handles nested structs at arbitrary depth.
func InjectDefaults(inputType reflect.Type, inputMap map[string]interface{}) error {
	if inputType.Kind() == reflect.Ptr {
		inputType = inputType.Elem()
	}

	if inputType.Kind() != reflect.Struct {
		return nil
	}

	return injectDefaultsRecursive(inputType, inputMap)
}

func injectDefaultsRecursive(structType reflect.Type, inputMap map[string]interface{}) error {
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Get JSON tag to determine map key
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		// Extract field name (before comma, which may have omitempty/squash etc)
		tagParts := strings.Split(jsonTag, ",")
		fieldName := tagParts[0]

		// Dereference pointer types to check underlying type
		fieldType := field.Type
		if fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}

		// Handle embedded fields marked with ",squash" (json:",squash")
		if fieldName == "" && containsTagOption(tagParts, "squash") {
			// For squash, inject defaults directly into the current map scope.
			if fieldType.Kind() == reflect.Struct {
				if err := injectDefaultsRecursive(fieldType, inputMap); err != nil {
					return err
				}
			}
			continue
		}

		// If no field name after processing, skip
		if fieldName == "" {
			continue
		}

		// Handle nested structs
		if fieldType.Kind() == reflect.Struct {
			// Get or create nested map
			var nestedMap map[string]interface{}
			if existing, exists := inputMap[fieldName]; exists {
				var ok bool
				nestedMap, ok = toStringKeyMap(existing)
				if !ok {
					// User provided a non-map value, skip recursion
					continue
				}
				// normalize in place if we converted
				inputMap[fieldName] = nestedMap
			} else {
				// Create nested map for defaults
				nestedMap = make(map[string]interface{})
				inputMap[fieldName] = nestedMap
			}

			// Recurse into nested struct
			if err := injectDefaultsRecursive(fieldType, nestedMap); err != nil {
				return err
			}

			// If nested map is empty after recursion, remove it
			if len(nestedMap) == 0 {
				delete(inputMap, fieldName)
			}

			continue
		}

		// Handle slices of structs (or ptr-to-struct)
		if fieldType.Kind() == reflect.Slice {
			elemType := fieldType.Elem()
			if elemType.Kind() == reflect.Ptr {
				elemType = elemType.Elem()
			}

			// Only recurse when element is a struct
			if elemType.Kind() == reflect.Struct {
				if raw, exists := inputMap[fieldName]; exists {
					// Support both []interface{} and []map[string]interface{} (or any slice kind)
					switch slice := raw.(type) {
					case []interface{}:
						for idx, item := range slice {
							if itemMap, ok := toStringKeyMap(item); ok {
								if err := injectDefaultsRecursive(elemType, itemMap); err != nil {
									return err
								}
								slice[idx] = itemMap
							}
						}
						inputMap[fieldName] = slice
					case []map[string]interface{}:
						for idx := range slice {
							if err := injectDefaultsRecursive(elemType, slice[idx]); err != nil {
								return err
							}
						}
						// keep original typed slice
						inputMap[fieldName] = slice
					default:
						val := reflect.ValueOf(raw)
						if val.Kind() == reflect.Slice {
							for i := 0; i < val.Len(); i++ {
								item := val.Index(i).Interface()
								if itemMap, ok := toStringKeyMap(item); ok {
									if err := injectDefaultsRecursive(elemType, itemMap); err != nil {
										return err
									}
									val.Index(i).Set(reflect.ValueOf(itemMap))
								}
							}
							inputMap[fieldName] = val.Interface()
						}
					}
				}
			}
		}

		// For non-struct fields, check if value already provided
		if _, exists := inputMap[fieldName]; exists {
			continue
		}

		// Check for default tag
		defaultValue := field.Tag.Get("default")
		if defaultValue == "" {
			continue
		}

		// Inject default as string (template resolver will handle type conversion)
		inputMap[fieldName] = defaultValue
	}

	return nil
}

// containsTagOption reports whether the JSON tag options include the given value.
func containsTagOption(parts []string, option string) bool {
	for _, p := range parts[1:] { // skip the field name portion
		if p == option {
			return true
		}
	}
	return false
}

// toStringKeyMap attempts to normalize various map types into map[string]interface{}.
// Returns (nil, false) if the input is not a compatible map.
func toStringKeyMap(v interface{}) (map[string]interface{}, bool) {
	switch m := v.(type) {
	case map[string]interface{}:
		return m, true
	case map[interface{}]interface{}:
		normalized := make(map[string]interface{}, len(m))
		for k, val := range m {
			keyStr, ok := k.(string)
			if !ok {
				return nil, false
			}
			normalized[keyStr] = val
		}
		return normalized, true
	default:
		return nil, false
	}
}
