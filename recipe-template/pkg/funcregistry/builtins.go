package funcregistry

import (
	"encoding/json"
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/itchyny/gojq"
)

// defaultBuiltins returns the builtin set equivalent to the previous hardcoded
// template configuration (json_parse, json_stringify, string overloads, jq).
func defaultBuiltins() map[string]BuiltinFactory {
	return map[string]BuiltinFactory{
		"json_parse": func(adapter types.Adapter, _ ContextProvider) cel.EnvOption {
			return cel.Function(
				"json_parse",
				cel.Overload(
					"json_parse_dyn",
					[]*cel.Type{cel.DynType},
					cel.DynType,
					cel.UnaryBinding(jsonParseBinding(adapter)),
				),
			)
		},
		"json_stringify": func(adapter types.Adapter, _ ContextProvider) cel.EnvOption {
			return cel.Function(
				"json_stringify",
				cel.Overload(
					"json_stringify_dyn",
					[]*cel.Type{cel.DynType},
					cel.StringType,
					cel.UnaryBinding(jsonStringifyBinding(adapter)),
				),
			)
		},
		"string_json_overloads": func(adapter types.Adapter, _ ContextProvider) cel.EnvOption {
			return cel.Function(
				"string",
				cel.Overload(
					"string_map_json",
					[]*cel.Type{cel.MapType(cel.DynType, cel.DynType)},
					cel.StringType,
					cel.UnaryBinding(func(val ref.Val) ref.Val {
						return jsonStringifyBinding(adapter)(val)
					}),
				),
				cel.Overload(
					"string_list_json",
					[]*cel.Type{cel.ListType(cel.DynType)},
					cel.StringType,
					cel.UnaryBinding(func(val ref.Val) ref.Val {
						return jsonStringifyBinding(adapter)(val)
					}),
				),
			)
		},
		"jq": func(adapter types.Adapter, _ ContextProvider) cel.EnvOption {
			return cel.Function(
				"jq",
				cel.Overload(
					"jq_dyn_string",
					[]*cel.Type{cel.DynType, cel.StringType},
					cel.DynType,
					cel.BinaryBinding(jqBinding(adapter)),
				),
				cel.MemberOverload(
					"jq_dyn_string_member",
					[]*cel.Type{cel.DynType, cel.StringType},
					cel.DynType,
					cel.BinaryBinding(jqBinding(adapter)),
				),
			)
		},
	}
}

func defaultTemplateBuiltins() map[string]TemplateFuncFactory {
	return map[string]TemplateFuncFactory{
		"json_parse": func(_ ContextProvider) interface{} {
			return func(input any) (any, error) {
				raw, ok := input.(string)
				if !ok {
					return nil, fmt.Errorf("json_parse: expected string")
				}
				if len(raw) == 0 {
					return nil, fmt.Errorf("json_parse: expected string")
				}
				var decoded interface{}
				if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
					return nil, fmt.Errorf("json_parse: invalid JSON: %w", err)
				}
				return decoded, nil
			}
		},
		"json_stringify": func(_ ContextProvider) interface{} {
			return func(value any) (string, error) {
				data, err := json.Marshal(value)
				if err != nil {
					return "", fmt.Errorf("json_stringify: failed to encode JSON: %w", err)
				}
				return string(data), nil
			}
		},
		"to_json": func(_ ContextProvider) interface{} {
			return func(value any) (string, error) {
				data, err := json.Marshal(value)
				if err != nil {
					return "", fmt.Errorf("to_json: failed to encode JSON: %w", err)
				}
				return string(data), nil
			}
		},
		"jq": func(_ ContextProvider) interface{} {
			return func(input any, expr string) (any, error) {
				parsed, err := gojq.Parse(expr)
				if err != nil {
					return nil, fmt.Errorf("jq: invalid expression: %w", err)
				}
				prog, err := gojq.Compile(parsed)
				if err != nil {
					return nil, fmt.Errorf("jq: compile failed: %w", err)
				}
				results, err := drainIter(prog.Run(input))
				if err != nil {
					return nil, fmt.Errorf("jq: execution failed: %w", err)
				}
				switch len(results) {
				case 0:
					return nil, nil
				case 1:
					return results[0], nil
				default:
					return results, nil
				}
			}
		},
	}
}

func jsonParseBinding(adapter types.Adapter) func(ref.Val) ref.Val {
	return func(value ref.Val) ref.Val {
		if value == nil {
			return types.NewErr("json_parse: expected string")
		}

		raw, ok := value.(types.String)
		if !ok {
			strValue, ok := value.Value().(string)
			if !ok {
				return types.NewErr("json_parse: expected string")
			}
			raw = types.String(strValue)
		}

		input := string(raw)
		if len(input) == 0 {
			return types.NewErr("json_parse: expected string")
		}

		var decoded interface{}
		if err := json.Unmarshal([]byte(input), &decoded); err != nil {
			return types.NewErr("json_parse: invalid JSON: %v", err)
		}

		return adapter.NativeToValue(decoded)
	}
}

func jsonStringifyBinding(adapter types.Adapter) func(ref.Val) ref.Val {
	return func(value ref.Val) ref.Val {
		native := normalizeJQInput(value)
		data, err := json.Marshal(native)
		if err != nil {
			return types.NewErr("json_stringify: failed to encode JSON: %v", err)
		}
		return types.String(data)
	}
}

func jqBinding(adapter types.Adapter) func(ref.Val, ref.Val) ref.Val {
	return func(input ref.Val, exprVal ref.Val) ref.Val {
		expr, ok := toString(exprVal)
		if !ok {
			return types.NewErr("jq: invalid expression: expected string")
		}

		parsed, err := gojq.Parse(expr)
		if err != nil {
			return types.NewErr("jq: invalid expression: %v", err)
		}

		prog, err := gojq.Compile(parsed)
		if err != nil {
			return types.NewErr("jq: compile failed: %v", err)
		}

		results, err := drainIter(prog.Run(normalizeJQInput(input)))
		if err != nil {
			return types.NewErr("jq: execution failed: %v", err)
		}

		switch len(results) {
		case 0:
			return types.NullValue
		case 1:
			return adapter.NativeToValue(results[0])
		default:
			return adapter.NativeToValue(results)
		}
	}
}

func normalizeJQInput(val ref.Val) interface{} {
	if val == nil {
		return nil
	}
	return val.Value()
}

func drainIter(iter gojq.Iter) ([]interface{}, error) {
	results := []interface{}{}
	for {
		val, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := val.(error); ok {
			return nil, err
		}
		if nested, ok := val.(gojq.Iter); ok {
			nestedResults, err := drainIter(nested)
			if err != nil {
				return nil, err
			}
			results = append(results, nestedResults...)
			continue
		}
		results = append(results, val)
	}
	return results, nil
}

func toString(val ref.Val) (string, bool) {
	if val == nil {
		return "", false
	}

	switch v := val.(type) {
	case types.String:
		return string(v), true
	default:
		if s, ok := v.Value().(string); ok {
			return s, true
		}
	}
	return "", false
}
