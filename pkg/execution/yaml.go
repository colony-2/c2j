package execution

import (
	"fmt"
	"gopkg.in/yaml.v3"
)

func (r *Requirements) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("execution requirements must be an object")
	}
	for i := 0; i < len(node.Content); i += 2 {
		name := node.Content[i].Value
		switch name {
		case "image", "platform":
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
	normalized, err := Requirements(decoded).Normalize()
	if err != nil {
		return err
	}
	*r = normalized
	return nil
}
