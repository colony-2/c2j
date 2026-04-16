package template

import (
    "reflect"

    "github.com/google/cel-go/common/types"
    "github.com/google/cel-go/common/types/ref"
    "github.com/google/cel-go/common/types/traits"
)

// DynamicOutputs marks a map whose keys are not known until runtime (e.g. child recipe outputs).
// During validation we want template resolution to tolerate missing keys and return a benign
// placeholder instead of failing with "no such key".
// DynamicOutputs is a marker type we use when seeding validation placeholders
// for steps whose outputs are not knowable at compile time (e.g., child recipes).
// It aliases a simple map so normal execution still gets a concrete map, while
// validation can wrap it in a permissive accessor.
type DynamicOutputs map[string]interface{}

// permissiveMap wraps a string->interface{} map and returns a placeholder value for unknown keys.
// This is only used for validation-time placeholder data.
type permissiveMap struct {
    adapter types.Adapter
    data    map[string]interface{}
}

func newPermissiveMap(data map[string]interface{}, adapter types.Adapter) ref.Val {
    return &permissiveMap{adapter: adapter, data: data}
}

func (m *permissiveMap) base() traits.Mapper {
    return types.NewStringInterfaceMap(m.adapter, m.data)
}

func (m *permissiveMap) Type() ref.Type {
    return types.NewMapType(types.StringType, types.DynType)
}

func (m *permissiveMap) Value() interface{} {
    return m.data
}

func (m *permissiveMap) Equal(other ref.Val) ref.Val {
    return m.base().Equal(other)
}

func (m *permissiveMap) ConvertToType(typeValue ref.Type) ref.Val {
    return m.base().ConvertToType(typeValue)
}

func (m *permissiveMap) ConvertToNative(typeDesc reflect.Type) (interface{}, error) {
    return m.base().ConvertToNative(typeDesc)
}

// Get returns the stored value if present, otherwise a nil placeholder instead of an error.
func (m *permissiveMap) Get(key ref.Val) ref.Val {
	if val, found := m.Find(key); found {
		return val
	}
	return types.NullValue
}

// Find mirrors the underlying map lookup but treats missing keys as present with a null value.
func (m *permissiveMap) Find(key ref.Val) (ref.Val, bool) {
	base := m.base()
	if val, found := base.Find(key); found {
		// Wrap nested maps so deeper selects stay permissive.
		if goVal, ok := val.Value().(map[string]interface{}); ok {
			return newPermissiveMap(goVal, m.adapter), true
		}
		return val, true
	}
	return types.NullValue, true
}

// Contains preserves normal membership semantics (missing keys return false).
func (m *permissiveMap) Contains(key ref.Val) ref.Val {
    return m.base().Contains(key)
}

func (m *permissiveMap) Iterator() traits.Iterator {
    return m.base().Iterator()
}

func (m *permissiveMap) Size() ref.Val {
    return m.base().Size()
}
