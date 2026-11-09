package recipe

import (
	"encoding/json"
	"fmt"

	"github.com/colony-2/c2j/pkg/execution"
	"gopkg.in/yaml.v3"
)

// ExecutionNeeds contains literal or templated node properties. Validation of
// resolved values uses the existing execution.Requirements model.
type ExecutionNeeds struct {
	CapabilitiesDeclared bool                   `json:"capabilities_declared,omitempty" yaml:"-"`
	Base                 any                    `yaml:"base,omitempty" json:"base,omitempty"`
	Capabilities         []string               `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Packages             []string               `yaml:"packages,omitempty" json:"packages,omitempty"`
	Resources            ExecutionNeedResources `yaml:"resources,omitempty" json:"resources,omitempty"`
	Image                any                    `yaml:"image,omitempty" json:"image,omitempty"`
	Platform             any                    `yaml:"platform,omitempty" json:"platform,omitempty"`
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
		case "base", "image", "platform", "packages":
		case "capabilities":
			n.CapabilitiesDeclared = true
			if err := execution.ValidateCapabilitiesYAML(node.Content[i+1]); err != nil {
				return err
			}
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
	if err := node.Decode((*plain)(n)); err != nil {
		return err
	}
	normalized, err := (execution.Requirements{Capabilities: n.Capabilities}).Normalize()
	if err != nil {
		return err
	}
	n.Capabilities = normalized.Capabilities
	return nil
}

// HasExecutionNeeds includes inline-expanded nodes and state bodies. It does
// not inspect separately submitted child recipes.
func HasExecutionNeeds(r Recipe) bool {
	return hasExecutionNeeds(r, func(n *ExecutionNeeds) bool { return n != nil })
}

// HasExecutionConstraints excludes packages: local setup does not require a
// resource-admission runtime when there are no capability/base/capacity constraints.
func HasExecutionConstraints(r Recipe) bool {
	return hasExecutionNeeds(r, func(n *ExecutionNeeds) bool {
		if n == nil {
			return false
		}
		base, literal := n.Base.(string)
		constrainedBase := n.Base != nil && (!literal || base != "*")
		return len(n.Capabilities) > 0 || constrainedBase || n.Image != nil || n.Platform != nil || n.Resources.CPU != nil || n.Resources.Memory != nil || n.Resources.EphemeralStorage != nil
	})
}
func hasExecutionNeeds(r Recipe, matches func(*ExecutionNeeds) bool) bool {
	if matches(r.GetMetadata().ExecutionNeeds) {
		return true
	}
	var nodeHas func(Node) bool
	nodeHas = func(n Node) bool {
		if _, shared := n.NodeImpl.(*NodeShared); shared {
			return false
		}
		if matches(n.GetMetadata().ExecutionNeeds) {
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

func (n ExecutionNeeds) MarshalYAML() (any, error) {
	type plain ExecutionNeeds
	var node yaml.Node
	if err := node.Encode(plain(n)); err != nil {
		return nil, err
	}
	if (n.CapabilitiesDeclared || n.Capabilities != nil) && len(n.Capabilities) == 0 {
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "capabilities"}, &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
	}
	return &node, nil
}

func (n ExecutionNeeds) MarshalJSON() ([]byte, error) {
	type plain ExecutionNeeds
	n.CapabilitiesDeclared = n.CapabilitiesDeclared || n.Capabilities != nil
	return json.Marshal(plain(n))
}
