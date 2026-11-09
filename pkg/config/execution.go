package config

import (
	"fmt"
	"github.com/colony-2/c2j/pkg/execution"
	"gopkg.in/yaml.v3"
	"strings"
)

// ExecutionDefaults accepts only literal portable execution requirements.
type ExecutionDefaults struct{ execution.Requirements }

func (e *ExecutionDefaults) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("execution: expected an object")
	}
	var r execution.Requirements
	scalar := func(n *yaml.Node, field string, cpu bool) (*string, error) {
		if n.Kind != yaml.ScalarNode || (n.Tag != "!!str" && !(cpu && (n.Tag == "!!int" || n.Tag == "!!float"))) || strings.TrimSpace(n.Value) == "" || strings.Contains(n.Value, "${{") || strings.Contains(n.Value, "{{") {
			return nil, fmt.Errorf("execution.%s: expected a nonempty literal", field)
		}
		v := n.Value
		return &v, nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		switch k {
		case "base":
			x, err := scalar(v, k, false)
			if err != nil {
				return err
			}
			r.Base = x
		case "capabilities":
			if err := execution.ValidateCapabilitiesYAML(v); err != nil {
				return fmt.Errorf("execution.capabilities: %w", err)
			}
			if err := v.Decode(&r.Capabilities); err != nil {
				return err
			}
		case "resources":
			if v.Kind != yaml.MappingNode {
				return fmt.Errorf("execution.resources: expected an object")
			}
			for j := 0; j < len(v.Content); j += 2 {
				name := v.Content[j].Value
				x, err := scalar(v.Content[j+1], "resources."+name, name == "cpu")
				if err != nil {
					return err
				}
				switch name {
				case "cpu":
					r.Resources.CPU = x
				case "memory":
					r.Resources.Memory = x
				case "ephemeral-storage":
					r.Resources.EphemeralStorage = x
				default:
					return fmt.Errorf("execution.resources: unknown field %q", name)
				}
			}
		default:
			return fmt.Errorf("execution: unknown field %q", k)
		}
	}
	normalized, err := r.Normalize()
	if err != nil {
		return err
	}
	e.Requirements = normalized
	return nil
}
func (c *ProjectConfig) Execution() execution.Requirements {
	return execution.Overlay(c.raw.Execution.Requirements, execution.Requirements{})
}

func validateExecutionDocument(raw []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "execution" && root.Content[i+1].Tag == "!!null" {
			return fmt.Errorf("execution: expected an object")
		}
	}
	return nil
}
