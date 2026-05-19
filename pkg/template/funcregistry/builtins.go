package funcregistry

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
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
		"nonempty": func(_ types.Adapter, _ ContextProvider) cel.EnvOption {
			return cel.Function(
				"nonempty",
				cel.Overload(
					"nonempty_any",
					[]*cel.Type{cel.AnyType},
					cel.BoolType,
					cel.UnaryBinding(func(value ref.Val) ref.Val {
						ok, errVal := nonemptyCEL(value)
						if errVal != nil {
							return errVal
						}
						return types.Bool(ok)
					}),
				),
			)
		},
		"first_nonempty": func(_ types.Adapter, _ ContextProvider) cel.EnvOption {
			overloads := make([]cel.FunctionOpt, 0, 10)
			for argc := 0; argc <= 10; argc++ {
				typesForArgs := make([]*cel.Type, argc)
				for i := range typesForArgs {
					typesForArgs[i] = cel.AnyType
				}
				overloads = append(overloads, cel.Overload(
					fmt.Sprintf("first_nonempty_any_%d", argc),
					typesForArgs,
					cel.DynType,
					cel.FunctionBinding(func(values ...ref.Val) ref.Val {
						return firstNonemptyCEL(values)
					}),
				))
			}
			return cel.Function("first_nonempty", overloads...)
		},
		"failure_is":               failureBoolCELFunc("failure_is", failureIs),
		"failure_message_contains": failureBoolCELFunc("failure_message_contains", failureMessageContains),
		"failure_message_matches":  failureBoolCELFunc("failure_message_matches", failureMessageMatches),
		"failure_has_code":         failureBoolCELFunc("failure_has_code", failureHasCode),
		"failure_originates_from":  failureBoolCELFunc("failure_originates_from", failureOriginatesFrom),
		"failure_root_cause": func(adapter types.Adapter, _ ContextProvider) cel.EnvOption {
			return cel.Function(
				"failure_root_cause",
				cel.Overload(
					"failure_root_cause_dyn",
					[]*cel.Type{cel.DynType},
					cel.DynType,
					cel.UnaryBinding(func(value ref.Val) ref.Val {
						return adapter.NativeToValue(toTemplateValue(failureRootCause(value.Value())))
					}),
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
		"nonempty": func(_ ContextProvider) interface{} {
			return func(value any) bool {
				return nonemptyNative(value)
			}
		},
		"first_nonempty": func(_ ContextProvider) interface{} {
			return func(values ...any) any {
				for _, value := range values {
					if nonemptyNative(value) {
						return value
					}
				}
				return nil
			}
		},
		"failure_is":               failureBoolTemplateFunc(failureIs),
		"failure_message_contains": failureBoolTemplateFunc(failureMessageContains),
		"failure_message_matches":  failureBoolTemplateFunc(failureMessageMatches),
		"failure_has_code":         failureBoolTemplateFunc(failureHasCode),
		"failure_originates_from":  failureBoolTemplateFunc(failureOriginatesFrom),
		"failure_root_cause": func(_ ContextProvider) interface{} {
			return func(value any) any {
				return toTemplateValue(failureRootCause(value))
			}
		},
	}
}

func failureBoolCELFunc(name string, impl func(any, string) bool) BuiltinFactory {
	return func(_ types.Adapter, _ ContextProvider) cel.EnvOption {
		return cel.Function(
			name,
			cel.Overload(
				name+"_dyn_string",
				[]*cel.Type{cel.DynType, cel.StringType},
				cel.BoolType,
				cel.BinaryBinding(func(lhs ref.Val, rhs ref.Val) ref.Val {
					text, ok := rhs.Value().(string)
					if !ok {
						return types.Bool(false)
					}
					return types.Bool(impl(lhs.Value(), text))
				}),
			),
		)
	}
}

func failureBoolTemplateFunc(impl func(any, string) bool) TemplateFuncFactory {
	return func(_ ContextProvider) interface{} {
		return func(value any, text string) bool {
			return impl(value, text)
		}
	}
}

func failureIs(value any, kind string) bool {
	f := decodeFailure(value)
	return f != nil && string(f.Kind) == kind
}

func failureMessageContains(value any, text string) bool {
	f := decodeFailure(value)
	return f != nil && strings.Contains(f.Message, text)
}

func failureMessageMatches(value any, expr string) bool {
	f := decodeFailure(value)
	if f == nil {
		return false
	}
	matched, err := regexp.MatchString(expr, f.Message)
	return err == nil && matched
}

func failureHasCode(value any, code string) bool {
	f := decodeFailure(value)
	return f != nil && f.Code == code
}

func failureOriginatesFrom(value any, nodePathOrID string) bool {
	for f := decodeFailure(value); f != nil; f = f.Cause {
		if f.Node.Path == nodePathOrID || f.Node.ID == nodePathOrID {
			return true
		}
	}
	return false
}

func failureRootCause(value any) any {
	f := decodeFailure(value)
	if f == nil {
		return nil
	}
	for f.Cause != nil {
		f = f.Cause
	}
	return f.Clone()
}

func decodeFailure(value any) *recipe.RuntimeFailure {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case *recipe.RuntimeFailure:
		return typed
	case recipe.RuntimeFailure:
		return &typed
	case ref.Val:
		return decodeFailure(typed.Value())
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var out recipe.RuntimeFailure
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	if out.Kind == "" && out.Message == "" && out.Node.Type == "" {
		return nil
	}
	return &out
}

func firstNonemptyCEL(values []ref.Val) ref.Val {
	for _, value := range values {
		ok, errVal := nonemptyCEL(value)
		if errVal != nil {
			return errVal
		}
		if ok {
			return unwrapOptionalCEL(value)
		}
	}
	return types.NullValue
}

func nonemptyCEL(value ref.Val) (bool, ref.Val) {
	value = unwrapOptionalCEL(value)
	if value == nil || value == types.NullValue {
		return false, nil
	}
	if types.IsError(value) {
		return false, value
	}

	switch v := value.(type) {
	case types.String:
		return strings.TrimSpace(string(v)) != "", nil
	case traits.Sizer:
		size := v.Size()
		if types.IsError(size) {
			return false, size
		}
		switch n := size.(type) {
		case types.Int:
			return n > 0, nil
		default:
			return nonemptyNative(size.Value()), nil
		}
	default:
		return nonemptyNative(value.Value()), nil
	}
}

func unwrapOptionalCEL(value ref.Val) ref.Val {
	if optional, ok := value.(*types.Optional); ok {
		if !optional.HasValue() {
			return types.NullValue
		}
		return optional.GetValue()
	}
	return value
}

func nonemptyNative(value any) bool {
	if value == nil {
		return false
	}
	if optional, ok := value.(*types.Optional); ok {
		if !optional.HasValue() {
			return false
		}
		return nonemptyNative(optional.GetValue())
	}
	if refValue, ok := value.(ref.Val); ok {
		ok, errVal := nonemptyCEL(refValue)
		return errVal == nil && ok
	}

	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return false
		}
		rv = rv.Elem()
		value = rv.Interface()
	}

	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v) != ""
	case []byte:
		return len(v) > 0
	}

	switch rv.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		return rv.Len() > 0
	default:
		return true
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
