package template

import (
	"reflect"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
)

const (
	validationArtifactPlaceholderJobID       = "__validation__"
	validationArtifactPlaceholderTaskOrdinal = int64(0)
	validationArtifactPlaceholderName        = "__artifact__"
)

// permissiveArtifactMap wraps artifact maps during validation and returns a
// placeholder artifact key for unknown names.
type permissiveArtifactMap struct {
	adapter types.Adapter
	data    map[string]recipeartifacts.Ref
}

func newPermissiveArtifactMap(data map[string]recipeartifacts.Ref, adapter types.Adapter) ref.Val {
	if data == nil {
		data = map[string]recipeartifacts.Ref{}
	}
	return &permissiveArtifactMap{adapter: adapter, data: data}
}

func (m *permissiveArtifactMap) base() traits.Mapper {
	baseData := make(map[string]interface{}, len(m.data))
	for name, artifact := range m.data {
		baseData[name] = artifact
	}
	return types.NewStringInterfaceMap(m.adapter, baseData)
}

func (m *permissiveArtifactMap) Type() ref.Type {
	return types.NewMapType(types.StringType, types.DynType)
}

func (m *permissiveArtifactMap) Value() interface{} {
	return m.data
}

func (m *permissiveArtifactMap) Equal(other ref.Val) ref.Val {
	return m.base().Equal(other)
}

func (m *permissiveArtifactMap) ConvertToType(typeValue ref.Type) ref.Val {
	return m.base().ConvertToType(typeValue)
}

func (m *permissiveArtifactMap) ConvertToNative(typeDesc reflect.Type) (interface{}, error) {
	return m.base().ConvertToNative(typeDesc)
}

func (m *permissiveArtifactMap) Get(key ref.Val) ref.Val {
	if val, found := m.Find(key); found {
		return val
	}
	return types.NullValue
}

func (m *permissiveArtifactMap) Find(key ref.Val) (ref.Val, bool) {
	base := m.base()
	if val, found := base.Find(key); found {
		// Existing artifacts in validation may still be in-memory and have no
		// persisted key. Fall back to a placeholder instead of erroring.
		if types.IsError(val) {
			return m.placeholderFor(key), true
		}
		return val, true
	}
	return m.placeholderFor(key), true
}

func (m *permissiveArtifactMap) Contains(key ref.Val) ref.Val {
	return m.base().Contains(key)
}

func (m *permissiveArtifactMap) Iterator() traits.Iterator {
	return m.base().Iterator()
}

func (m *permissiveArtifactMap) Size() ref.Val {
	return m.base().Size()
}

func (m *permissiveArtifactMap) placeholderFor(key ref.Val) ref.Val {
	name := artifactNameFromMapKey(key)
	if name == "" {
		name = validationArtifactPlaceholderName
	}
	placeholder := swf.ArtifactKey{
		JobId:       validationArtifactPlaceholderJobID,
		TaskOrdinal: validationArtifactPlaceholderTaskOrdinal,
		Name:        name,
		SizeBytes:   -1,
	}
	return m.adapter.NativeToValue(recipeartifacts.NewStoredRef(placeholder))
}

func artifactNameFromMapKey(key ref.Val) string {
	if key == nil {
		return ""
	}
	if s, ok := key.Value().(string); ok {
		return s
	}
	native, err := key.ConvertToNative(reflect.TypeOf(""))
	if err != nil {
		return ""
	}
	s, _ := native.(string)
	return s
}
