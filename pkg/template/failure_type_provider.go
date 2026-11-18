package template

import (
	"fmt"

	"github.com/colony-2/c2j/pkg/grouping"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/google/cel-go/common/types"
)

// NativeTypes does not expose map[string]any fields. Keep the failure envelope
// statically checked while allowing versioned operation-specific attributes.
type failureTypeProvider struct{ types.Provider }

func (p failureTypeProvider) FindStructFieldType(name, field string) (*types.FieldType, bool) {
	if name == "grouping.Contribution" && field == "content" {
		content := func(obj any) (any, error) {
			switch c := obj.(type) {
			case grouping.Contribution:
				return c.Content, nil
			case *grouping.Contribution:
				if c != nil {
					return c.Content, nil
				}
			}
			return nil, fmt.Errorf("invalid grouping contribution %T", obj)
		}
		return &types.FieldType{Type: types.NewMapType(types.StringType, types.DynType), GetFrom: content, IsSet: func(obj any) bool {
			v, err := content(obj)
			if err != nil {
				return false
			}
			m, ok := v.(map[string]any)
			return ok && m != nil
		}}, true
	}
	if name != "recipe.RuntimeFailure" || field != "attrs" {
		return p.Provider.FindStructFieldType(name, field)
	}
	attrs := func(obj any) (any, error) {
		switch f := obj.(type) {
		case recipe.RuntimeFailure:
			return f.Attrs, nil
		case *recipe.RuntimeFailure:
			if f != nil {
				return f.Attrs, nil
			}
		case map[string]any:
			return f["attrs"], nil
		}
		return nil, fmt.Errorf("invalid failure value %T", obj)
	}
	return &types.FieldType{Type: types.NewMapType(types.StringType, types.DynType), GetFrom: attrs, IsSet: func(obj any) bool {
		v, err := attrs(obj)
		if err != nil || v == nil {
			return false
		}
		m, ok := v.(map[string]any)
		return ok && len(m) != 0
	}}, true
}
