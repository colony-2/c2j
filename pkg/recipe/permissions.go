package recipe

import (
	"fmt"
	"reflect"

	"github.com/colony-2/c2j/pkg/permissions"
	"gopkg.in/yaml.v3"
)

func decodePerms(n *yaml.Node, impl any) error {
	seen := false
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != "perms" {
			continue
		}
		if seen {
			return fmt.Errorf("duplicate perms")
		}
		seen = true
		var spec permissions.Spec
		if err := spec.UnmarshalYAML(n.Content[i+1]); err != nil {
			return err
		}
		field := reflect.ValueOf(impl).Elem().FieldByName("Perms")
		if !field.IsValid() || !field.CanSet() {
			return fmt.Errorf("perms belongs on the shared definition or enclosing node")
		}
		field.Set(reflect.ValueOf(&spec))
	}
	return nil
}

// HasPermissions enables the versioned contract for the whole recipe, so nodes
// before an explicit declaration receive the same restricted defaults.
func HasPermissions(r Recipe) bool {
	if r.GetMetdata().Perms != nil {
		return true
	}
	var has func(Node) bool
	has = func(n Node) bool {
		if _, shared := n.NodeImpl.(*NodeShared); shared {
			return false
		}
		if n.GetMetadata().Perms != nil {
			return true
		}
		switch v := n.NodeImpl.(type) {
		case *NodeSequence:
			for _, child := range v.Sequence {
				if has(child) {
					return true
				}
			}
		case *NodeState:
			if v.States != nil {
				for _, child := range v.States.States {
					if has(child.Node) {
						return true
					}
				}
			}
		}
		return false
	}
	for _, def := range r.GetMetdata().Defs {
		if has(def) {
			return true
		}
	}
	switch v := r.RecipeImpl.(type) {
	case *RecipeSequence:
		for _, child := range v.Sequence {
			if has(child) {
				return true
			}
		}
	case *RecipeState:
		if v.States != nil {
			for _, child := range v.States.States {
				if has(child.Node) {
					return true
				}
			}
		}
	}
	return false
}
