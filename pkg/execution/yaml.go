package execution

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// MarshalYAML makes programmatically constructed and parsed declarations share
// the same representation, including when hashing a pinned recipe.
func (r Requirements) MarshalYAML() (any, error) {
	normalized, err := r.Normalize()
	if err != nil {
		return nil, err
	}
	type plain Requirements
	var node yaml.Node
	if err := node.Encode(plain(normalized)); err != nil {
		return nil, err
	}
	if normalized.CapabilitiesDeclared && len(normalized.Capabilities) == 0 {
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "capabilities"}, &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
	}
	return &node, nil
}

func (r *Requirements) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("execution requirements must be an object")
	}
	for i := 0; i < len(node.Content); i += 2 {
		name := node.Content[i].Value
		switch name {
		case "python":
			if err := ValidatePythonYAML(node.Content[i+1]); err != nil {
				return err
			}
		case "base", "image", "platform", "packages":
		case "capabilities":
			if err := ValidateCapabilitiesYAML(node.Content[i+1]); err != nil {
				return err
			}
		case "resources":
			resources := node.Content[i+1]
			if resources.Kind != yaml.MappingNode {
				return fmt.Errorf("execution resources must be an object")
			}
			for j := 0; j < len(resources.Content); j += 2 {
				key := resources.Content[j].Value
				if key != "cpu" && key != "memory" && key != "ephemeral-storage" {
					return fmt.Errorf("unknown execution resource %q", key)
				}
			}
		default:
			return fmt.Errorf("unknown execution requirement %q", name)
		}
	}
	type plain Requirements
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	decoded.CapabilitiesDeclared = decoded.Capabilities != nil && len(decoded.Capabilities) == 0
	normalized, err := Requirements(decoded).Normalize()
	if err != nil {
		return err
	}
	*r = normalized
	return nil
}
