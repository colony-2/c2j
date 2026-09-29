package recipe

import (
	"fmt"
	"strings"

	"github.com/invopop/jsonschema"
	"gopkg.in/yaml.v3"
)

// WorkspaceSpec starts an isolated, durable workspace for a node invocation.
type WorkspaceSpec struct {
	Cell string `yaml:"cell" json:"cell"`
	Ref  string `yaml:"ref,omitempty" json:"ref,omitempty"`
}

func (s *WorkspaceSpec) UnmarshalYAML(n *yaml.Node) error {
	*s = WorkspaceSpec{}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("workspace must be an object with cell and optional ref")
	}
	seen := map[string]bool{}
	for i := 0; i < len(n.Content); i += 2 {
		key, val := n.Content[i].Value, n.Content[i+1]
		if seen[key] {
			return fmt.Errorf("duplicate workspace.%s", key)
		}
		seen[key] = true
		if key != "cell" && key != "ref" {
			return fmt.Errorf("unknown workspace field %q", key)
		}
		if val.Kind != yaml.ScalarNode || val.Tag != "!!str" || strings.TrimSpace(val.Value) == "" {
			return fmt.Errorf("workspace.%s must be a nonempty string", key)
		}
		if key == "cell" {
			s.Cell = val.Value
		} else {
			s.Ref = val.Value
		}
	}
	if strings.TrimSpace(s.Cell) == "" {
		return fmt.Errorf("workspace.cell is required")
	}
	return nil
}

func (WorkspaceSpec) JSONSchema() *jsonschema.Schema {
	min := uint64(1)
	props := jsonschema.NewProperties()
	props.Set("cell", &jsonschema.Schema{Type: "string", MinLength: &min})
	props.Set("ref", &jsonschema.Schema{Type: "string", MinLength: &min})
	return &jsonschema.Schema{Type: "object", Properties: props, Required: []string{"cell"}, AdditionalProperties: jsonschema.FalseSchema}
}

func HasWorkspaces(r Recipe) bool {
	if r.GetMetadata().Workspace != nil {
		return true
	}
	var nodeHas func(Node) bool
	nodeHas = func(n Node) bool {
		if _, shared := n.NodeImpl.(*NodeShared); shared {
			return false
		}
		if n.GetMetadata().Workspace != nil {
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

func validateWorkspaceField(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value != "workspace" {
			continue
		}
		var spec WorkspaceSpec
		if err := spec.UnmarshalYAML(node.Content[i+1]); err != nil {
			return err
		}
		for j := 0; j < len(node.Content); j += 2 {
			if node.Content[j].Value == "shared" {
				return fmt.Errorf("workspace belongs on the shared definition or a containing node")
			}
		}
	}
	return nil
}
