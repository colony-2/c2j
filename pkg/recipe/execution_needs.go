package recipe

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ExecutionNeeds contains literal or templated node properties. Validation of
// resolved values uses the existing execution.Requirements model.
type ExecutionNeeds struct {
	Resources ExecutionNeedResources `yaml:"resources,omitempty" json:"resources,omitempty"`
	Image     any                    `yaml:"image,omitempty" json:"image,omitempty"`
	Platform  any                    `yaml:"platform,omitempty" json:"platform,omitempty"`
}

type ExecutionNeedResources struct {
	CPU              any `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	Memory           any `yaml:"memory,omitempty" json:"memory,omitempty"`
	EphemeralStorage any `yaml:"ephemeral-storage,omitempty" json:"ephemeral-storage,omitempty"`
}

func (n *ExecutionNeeds) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("execution_needs must be an object")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		switch key {
		case "image", "platform":
		case "resources":
			r := node.Content[i+1]
			if r.Kind != yaml.MappingNode {
				return fmt.Errorf("execution_needs.resources must be an object")
			}
			for j := 0; j < len(r.Content); j += 2 {
				k := r.Content[j].Value
				if k != "cpu" && k != "memory" && k != "ephemeral-storage" {
					return fmt.Errorf("unknown execution_needs.resources property %q", k)
				}
			}
		default:
			return fmt.Errorf("unknown execution_needs property %q", key)
		}
	}
	type plain ExecutionNeeds
	return node.Decode((*plain)(n))
}

// HasExecutionNeeds includes inline-expanded nodes and state bodies. It does
// not inspect separately submitted child recipes.
func HasExecutionNeeds(r Recipe) bool {
	if r.GetMetadata().ExecutionNeeds != nil {
		return true
	}
	var nodeHas func(Node) bool
	nodeHas = func(n Node) bool {
		if _, shared := n.NodeImpl.(*NodeShared); shared {
			return false
		}
		if n.GetMetadata().ExecutionNeeds != nil {
			return true
		}
		switch v := n.NodeImpl.(type) {
		case *NodeSequence:
			for _, child := range v.Sequence {
				if nodeHas(child) {
					return true
				}
			}
		case *NodeState:
			if v.States != nil {
				for _, state := range v.States.States {
					if nodeHas(state.Node) {
						return true
					}
				}
			}
		}
		return false
	}
	switch v := r.RecipeImpl.(type) {
	case *RecipeSequence:
		for _, child := range v.Sequence {
			if nodeHas(child) {
				return true
			}
		}
	case *RecipeState:
		if v.States != nil {
			for _, state := range v.States.States {
				if nodeHas(state.Node) {
					return true
				}
			}
		}
	}
	return false
}
