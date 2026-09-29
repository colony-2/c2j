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
		if v == nil {
			return v
		}
		out := make(map[any]any, len(v))
		for key, item := range v {
			out[nativeTemplateResult(key)] = nativeTemplateResult(item)
		}
		return out
	case map[any]any:
		if v == nil {
			return v
		}
		out := make(map[any]any, len(v))
		for key, item := range v {
			out[nativeTemplateResult(key)] = nativeTemplateResult(item)
		}
		return out
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
