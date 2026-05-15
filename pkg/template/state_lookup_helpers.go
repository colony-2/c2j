package template

import (
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

func (rc *ResolutionContext) stateLookupEnvOptions(adapter types.Adapter) []cel.EnvOption {
	return []cel.EnvOption{
		cel.Function(
			"state_exists",
			cel.Overload(
				"state_exists_string",
				[]*cel.Type{cel.StringType},
				cel.BoolType,
				cel.UnaryBinding(func(id ref.Val) ref.Val {
					stateID, ok := refString(id)
					if !ok {
						return types.NewErr("state_exists: id must be a string")
					}
					return types.Bool(rc.stateExists(stateID))
				}),
			),
		),
		cel.Function(
			"state_output",
			cel.Overload(
				"state_output_string_string_any",
				[]*cel.Type{cel.StringType, cel.StringType, cel.AnyType},
				cel.DynType,
				cel.FunctionBinding(func(args ...ref.Val) ref.Val {
					stateID, ok := refString(args[0])
					if !ok {
						return types.NewErr("state_output: id must be a string")
					}
					path, ok := refString(args[1])
					if !ok {
						return types.NewErr("state_output: path must be a string")
					}
					if value, found := rc.stateOutput(stateID, path); found {
						return adapter.NativeToValue(value)
					}
					return args[2]
				}),
			),
		),
		cel.Function(
			"state_field",
			cel.Overload(
				"state_field_string_string_any",
				[]*cel.Type{cel.StringType, cel.StringType, cel.AnyType},
				cel.DynType,
				cel.FunctionBinding(func(args ...ref.Val) ref.Val {
					stateID, ok := refString(args[0])
					if !ok {
						return types.NewErr("state_field: id must be a string")
					}
					fieldID, ok := refString(args[1])
					if !ok {
						return types.NewErr("state_field: field must be a string")
					}
					if value, found := rc.stateField(stateID, fieldID); found {
						return adapter.NativeToValue(value)
					}
					return args[2]
				}),
			),
		),
	}
}

func (rc *ResolutionContext) stateLookupTemplateFuncs() map[string]interface{} {
	return map[string]interface{}{
		"state_exists": func(id string) bool {
			return rc.stateExists(id)
		},
		"state_output": func(id string, path string, fallback any) any {
			if value, found := rc.stateOutput(id, path); found {
				return value
			}
			return fallback
		},
		"state_field": func(id string, field string, fallback any) any {
			if value, found := rc.stateField(id, field); found {
				return value
			}
			return fallback
		},
	}
}

func (rc *ResolutionContext) stateExists(id string) bool {
	if strings.TrimSpace(id) == "" {
		return false
	}
	step, ok := rc.TemplateData.States[id]
	if !ok {
		return false
	}
	return !isValidationPlaceholderState(step)
}

func (rc *ResolutionContext) stateOutput(id string, path string) (any, bool) {
	if !rc.stateExists(id) {
		return nil, false
	}
	step := rc.TemplateData.States[id]
	if strings.TrimSpace(path) == "" {
		return step.Outputs, true
	}

	var current any = step.Outputs
	for _, segment := range strings.Split(path, ".") {
		if segment == "" {
			return nil, false
		}
		asMap, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		next, found := asMap[segment]
		if !found {
			return nil, false
		}
		current = next
	}
	return current, true
}

func (rc *ResolutionContext) stateField(id string, field string) (any, bool) {
	if strings.TrimSpace(field) == "" {
		return nil, false
	}
	if !rc.stateExists(id) {
		return nil, false
	}
	step := rc.TemplateData.States[id]
	fields, ok := step.Outputs["fields"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	value, found := fields[field]
	return value, found
}

func isValidationPlaceholderState(step StepOutput) bool {
	if len(step.Runs) != 1 {
		return false
	}
	run := step.Runs[0]
	return run.RunID == "" && run.Timestamp.IsZero()
}

func refString(value ref.Val) (string, bool) {
	if value == nil {
		return "", false
	}
	switch v := value.(type) {
	case types.String:
		return string(v), true
	default:
		s, ok := value.Value().(string)
		return s, ok
	}
}
