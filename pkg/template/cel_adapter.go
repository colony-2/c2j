package template

import (
	"encoding/json"
	"reflect"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
)

type resolutionTypeAdapter struct {
	base    types.Adapter
	options ResolutionOptions
}

func newResolutionTypeAdapter(base types.Adapter, options ResolutionOptions) types.Adapter {
	return &resolutionTypeAdapter{
		base:    base,
		options: options,
	}
}

func (a *resolutionTypeAdapter) NativeToValue(value interface{}) ref.Val {
	// When validation placeholders mark dynamic outputs, wrap them so missing
	// keys resolve to null instead of throwing "no such key" at compile time.
	if dyn, ok := value.(DynamicOutputs); ok {
		return newPermissiveMap(dyn, a)
	}

	if ctx, ok := value.(contextual.TaskExecutionContext); ok {
		data, err := json.Marshal(ctx)
		if err != nil {
			return types.NewErr("failed to marshal context: %v", err)
		}
		var flattened map[string]interface{}
		if err := json.Unmarshal(data, &flattened); err != nil {
			return types.NewErr("failed to unmarshal context: %v", err)
		}
		return a.base.NativeToValue(flattened)
	}

	if v, ok := value.(ref.Val); ok {
		if a.options.ClampSliceIndex {
			if list, ok := v.(traits.Lister); ok {
				return newClampedList(list)
			}
		}
		return v
	}

	if a.options.ClampSliceIndex {
		valueType := reflect.TypeOf(value)
		if valueType != nil && (valueType.Kind() == reflect.Slice || valueType.Kind() == reflect.Array) {
			if valueType.Kind() == reflect.Slice && valueType.Elem().Kind() == reflect.Uint8 {
				return a.base.NativeToValue(value)
			}
			return newClampedList(types.NewDynamicList(a, value))
		}
	}

	return a.base.NativeToValue(value)
}

type clampedList struct {
	traits.Lister
}

func newClampedList(list traits.Lister) ref.Val {
	if _, ok := list.(*clampedList); ok {
		return list
	}
	return &clampedList{Lister: list}
}

func (c *clampedList) Get(_ ref.Val) ref.Val {
	return c.Lister.Get(types.IntZero)
}
