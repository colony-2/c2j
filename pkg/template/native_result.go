package template

import (
	"github.com/google/cel-go/common/types/ref"
	"google.golang.org/protobuf/types/known/structpb"
)

// CEL container Value() methods may still contain nested ref.Val wrappers.
// Unwrap them before results reach JSON encoding, while retaining native
// artifact structs and integer precision (no JSON round-trip).
func nativeTemplateResult(value any) any {
	switch v := value.(type) {
	case ref.Val:
		return nativeTemplateResult(v.Value())
	case structpb.NullValue:
		return nil
	case map[string]any:
		if v == nil {
			return v
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = nativeTemplateResult(item)
		}
		return out
	case map[ref.Val]ref.Val:
		return nativeTemplateMap(v)
	case map[any]any:
		return nativeTemplateMap(v)
	case []any:
		if v == nil {
			return v
		}
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = nativeTemplateResult(item)
		}
		return out
	case []ref.Val:
		if v == nil {
			return v
		}
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = nativeTemplateResult(item)
		}
		return out
	default:
		return value
	}
}

// CEL literals and dynamic maps expose interface-typed keys even for JSON
// objects. Normalize string-keyed maps (including empty maps) to JSON-compatible
// objects. Keep actual non-string keys intact rather than stringify and risk
// collisions, e.g. CEL keys 1 and "1".
func nativeTemplateMap[K comparable, V any](value map[K]V) any {
	if value == nil {
		return map[string]any(nil)
	}
	converted := make(map[any]any, len(value))
	stringsOnly := true
	for key, item := range value {
		nativeKey := nativeTemplateResult(key)
		if _, ok := nativeKey.(string); !ok {
			stringsOnly = false
		}
		converted[nativeKey] = nativeTemplateResult(item)
	}
	if !stringsOnly {
		return converted
	}
	result := make(map[string]any, len(converted))
	for key, item := range converted {
		result[key.(string)] = item
	}
	return result
}
