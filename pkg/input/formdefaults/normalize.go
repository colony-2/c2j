package formdefaults

import (
	"fmt"
	"reflect"
)

const singleQuestionFieldID = "response"

type Config struct {
	Question   string
	Type       string
	Default    interface{}
	HasDefault bool
	ScaleMin   int
	HasScale   bool
	Fields     []Field
}

type Field struct {
	ID         string
	Type       string
	Required   bool
	Default    interface{}
	HasDefault bool
	ScaleMin   int
	HasScale   bool
}

type Output struct {
	Response        interface{}
	ResponsePresent bool
	Fields          map[string]interface{}
	UserID          string
	Metadata        map[string]interface{}
}

func NormalizeOutput(form Config, out Output) (Output, error) {
	normalized := Output{
		Response:        cloneValue(out.Response),
		ResponsePresent: out.ResponsePresent,
		Fields:          cloneStringInterfaceMap(out.Fields),
		UserID:          out.UserID,
		Metadata:        cloneStringInterfaceMap(out.Metadata),
	}
	if normalized.Fields == nil {
		normalized.Fields = map[string]interface{}{}
	}
	if err := normalizeOutputParts(form, &normalized); err != nil {
		return Output{}, err
	}
	return normalized, nil
}

// NormalizeOutputMap accepts the normalized input-op invocation map and the op
// output map, then returns a canonical input Output map.
func NormalizeOutputMap(opInput map[string]interface{}, opOutput map[string]interface{}) (map[string]interface{}, error) {
	form, ok := configFromInputMap(opInput)
	if !ok {
		return opOutput, nil
	}

	parts := outputPartsFromMap(opOutput)
	normalized, err := NormalizeOutput(form, parts)
	if err != nil {
		return nil, err
	}
	return outputAsMap(normalized), nil
}

// ValidationOutputMap synthesizes an input output for semantic validation. It
// preserves the normal output shape while marking required fields as present so
// validation can proceed without a submitted human response.
func ValidationOutputMap(opInput map[string]interface{}) (map[string]interface{}, bool) {
	form, ok := configFromInputMap(opInput)
	if !ok {
		return nil, false
	}

	out := Output{Fields: map[string]interface{}{}}
	if len(form.Fields) > 0 {
		for _, field := range form.Fields {
			if field.ID == "" {
				continue
			}
			if value, ok := validationFieldValue(field); ok {
				out.Fields[field.ID] = value
			}
		}
		return outputAsMap(out), true
	}

	if form.Question == "" {
		return outputAsMap(out), true
	}
	if value, ok := validationSingleQuestionValue(form); ok {
		out.Response = value
		out.ResponsePresent = true
		out.Fields[singleQuestionFieldID] = cloneValue(value)
	}
	return outputAsMap(out), true
}

func normalizeOutputParts(form Config, parts *Output) error {
	if parts.Fields == nil {
		parts.Fields = map[string]interface{}{}
	}

	if len(form.Fields) > 0 {
		for _, field := range form.Fields {
			if field.ID == "" {
				continue
			}
			if _, exists := parts.Fields[field.ID]; exists {
				continue
			}
			value, ok := fieldDefaultValue(field)
			if ok {
				parts.Fields[field.ID] = value
				continue
			}
			if field.Required {
				return fmt.Errorf("required input field %q missing from output", field.ID)
			}
		}
		return nil
	}

	if form.Question == "" {
		return nil
	}

	fieldValue, fieldPresent := parts.Fields[singleQuestionFieldID]
	switch {
	case parts.ResponsePresent && !fieldPresent:
		parts.Fields[singleQuestionFieldID] = cloneValue(parts.Response)
	case !parts.ResponsePresent && fieldPresent:
		parts.Response = cloneValue(fieldValue)
		parts.ResponsePresent = true
	case !parts.ResponsePresent && !fieldPresent:
		value, ok := singleQuestionDefaultValue(form)
		if ok {
			parts.Response = cloneValue(value)
			parts.ResponsePresent = true
			parts.Fields[singleQuestionFieldID] = cloneValue(value)
		}
	}
	return nil
}

func fieldDefaultValue(field Field) (interface{}, bool) {
	if field.HasDefault {
		return cloneValue(field.Default), true
	}

	if field.Required {
		return nil, false
	}

	return implicitDefaultValue(field.Type, field.ScaleMin, field.HasScale)
}

func validationFieldValue(field Field) (interface{}, bool) {
	if value, ok := fieldDefaultValue(field); ok {
		return value, true
	}
	return implicitDefaultValue(field.Type, field.ScaleMin, field.HasScale)
}

func singleQuestionDefaultValue(form Config) (interface{}, bool) {
	if form.HasDefault {
		return cloneValue(form.Default), true
	}
	return implicitDefaultValue(form.Type, form.ScaleMin, form.HasScale)
}

func validationSingleQuestionValue(form Config) (interface{}, bool) {
	if value, ok := singleQuestionDefaultValue(form); ok {
		return value, true
	}
	return implicitDefaultValue(form.Type, form.ScaleMin, form.HasScale)
}

func implicitDefaultValue(fieldType string, scaleMin int, hasScale bool) (interface{}, bool) {
	switch fieldType {
	case "", "short_answer", "paragraph_text", "multiple_choice", "dropdown", "date", "time":
		return "", true
	case "checkboxes":
		return []interface{}{}, true
	case "boolean":
		return false, true
	case "linear_scale":
		if !hasScale {
			return nil, false
		}
		return scaleMin, true
	default:
		return nil, false
	}
}

func configFromInputMap(opInput map[string]interface{}) (Config, bool) {
	if opInput == nil {
		return Config{}, false
	}
	rawForm, ok := asStringInterfaceMap(opInput["form"])
	if !ok {
		return Config{}, false
	}
	return configFromFormMap(rawForm), true
}

func configFromFormMap(rawForm map[string]interface{}) Config {
	out := Config{}
	if question, ok := rawForm["question"].(string); ok {
		out.Question = question
	}
	if fieldType, ok := rawForm["type"].(string); ok {
		out.Type = fieldType
	}
	if defaultValue, ok := rawForm["default"]; ok {
		out.Default = cloneValue(defaultValue)
		out.HasDefault = true
	}
	if scale, ok := scaleMinFromMap(rawForm["scale"]); ok {
		out.ScaleMin = scale
		out.HasScale = true
	}
	if rawFields, ok := sliceFromAny(rawForm["fields"]); ok {
		out.Fields = make([]Field, 0, len(rawFields))
		for _, rawField := range rawFields {
			fieldMap, ok := asStringInterfaceMap(rawField)
			if !ok {
				continue
			}
			out.Fields = append(out.Fields, fieldFromMap(fieldMap))
		}
	}
	return out
}

func fieldFromMap(rawField map[string]interface{}) Field {
	out := Field{}
	if id, ok := rawField["id"].(string); ok {
		out.ID = id
	}
	if fieldType, ok := rawField["type"].(string); ok {
		out.Type = fieldType
	}
	if required, ok := rawField["required"].(bool); ok {
		out.Required = required
	}
	if defaultValue, ok := rawField["default"]; ok {
		out.Default = cloneValue(defaultValue)
		out.HasDefault = true
	}
	if scale, ok := scaleMinFromMap(rawField["scale"]); ok {
		out.ScaleMin = scale
		out.HasScale = true
	}
	return out
}

func scaleMinFromMap(value interface{}) (int, bool) {
	scale, ok := asStringInterfaceMap(value)
	if !ok {
		return 0, false
	}
	return intFromAny(scale["min"])
}

func intFromAny(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func outputPartsFromMap(opOutput map[string]interface{}) Output {
	parts := Output{}
	if opOutput == nil {
		return parts
	}
	if value, ok := opOutput["response"]; ok {
		parts.Response = cloneValue(value)
		parts.ResponsePresent = true
	}
	if fields, ok := asStringInterfaceMap(opOutput["fields"]); ok {
		parts.Fields = cloneStringInterfaceMap(fields)
	}
	if userID, ok := opOutput["user_id"].(string); ok {
		parts.UserID = userID
	}
	if metadata, ok := asStringInterfaceMap(opOutput["metadata"]); ok {
		parts.Metadata = cloneStringInterfaceMap(metadata)
	}
	return parts
}

func outputAsMap(out Output) map[string]interface{} {
	result := map[string]interface{}{
		"fields": out.Fields,
	}
	if out.ResponsePresent {
		result["response"] = out.Response
	}
	if out.UserID != "" {
		result["user_id"] = out.UserID
	}
	if len(out.Metadata) > 0 {
		result["metadata"] = out.Metadata
	}
	return result
}

func asStringInterfaceMap(value interface{}) (map[string]interface{}, bool) {
	switch typed := value.(type) {
	case map[string]interface{}:
		return typed, true
	default:
		rv := reflect.ValueOf(value)
		if !rv.IsValid() || rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
			return nil, false
		}
		out := make(map[string]interface{}, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = iter.Value().Interface()
		}
		return out, true
	}
}

func sliceFromAny(value interface{}) ([]interface{}, bool) {
	switch typed := value.(type) {
	case []interface{}:
		return typed, true
	default:
		rv := reflect.ValueOf(value)
		if !rv.IsValid() || rv.Kind() != reflect.Slice {
			return nil, false
		}
		out := make([]interface{}, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out = append(out, rv.Index(i).Interface())
		}
		return out, true
	}
}

func cloneStringInterfaceMap(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneStringInterfaceMap(typed)
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return typed
	}
}
