package recipe

import (
	"encoding/json"

	"github.com/invopop/jsonschema"
	"gopkg.in/yaml.v3"
)

// StatusTemplate holds a recipe-authored JSON-shaped value. A nil declaration
// is omitted; a non-nil declaration with a nil Value is explicit null.
type StatusTemplate struct{ Value any }

func (status StatusTemplate) MarshalYAML() (any, error)      { return status.Value, nil }
func (status StatusTemplate) MarshalJSON() ([]byte, error)   { return json.Marshal(status.Value) }
func (status StatusTemplate) JSONSchema() *jsonschema.Schema { return &jsonschema.Schema{} }
func (status *StatusTemplate) UnmarshalYAML(node *yaml.Node) error {
	return node.Decode(&status.Value)
}

func (meta *NodeMetadata) preserveNullStatus(fields map[string]any) {
	preserveNullStatus(fields, &meta.Status)
}

func (shared *NodeShared) preserveNullStatus(fields map[string]any) {
	preserveNullStatus(fields, &shared.Status)
}

func preserveNullStatus(fields map[string]any, target **StatusTemplate) {
	if value, declared := fields["status"]; declared && value == nil {
		*target = &StatusTemplate{}
	}
}

func overrideSharedStatus(node Node, status *StatusTemplate) Node {
	if status == nil {
		return node
	}
	return withNodeStatus(node, status)
}

func withNodeStatus(node Node, status *StatusTemplate) Node {
	switch impl := node.NodeImpl.(type) {
	case *NodeOp:
		clone := *impl
		clone.Status = status
		node.NodeImpl = &clone
	case *NodeSequence:
		clone := *impl
		clone.Status = status
		node.NodeImpl = &clone
	case *NodeState:
		clone := *impl
		clone.Status = status
		node.NodeImpl = &clone
	case *NodeParallel:
		clone := *impl
		clone.Status = status
		node.NodeImpl = &clone
	case *NodeChildGroup:
		clone := *impl
		clone.Status = status
		node.NodeImpl = &clone
	case *NodeInclude:
		clone := *impl
		clone.Status = status
		node.NodeImpl = &clone
	case *NodeShared:
		clone := *impl
		clone.Status = status
		node.NodeImpl = &clone
	}
	return node
}

func WithoutNodeStatus(node Node) Node {
	result := withNodeStatus(node, nil)
	clear := func(nodes []Node) []Node {
		if nodes == nil {
			return nil
		}
		children := make([]Node, len(nodes))
		for index, child := range nodes {
			children[index] = WithoutNodeStatus(child)
		}
		return children
	}
	switch impl := result.NodeImpl.(type) {
	case *NodeSequence:
		impl.Sequence = clear(impl.Sequence)
	case *NodeParallel:
		impl.Parallel.Branches = clear(impl.Parallel.Branches)
	case *NodeState:
		if impl.States != nil {
			states := *impl.States
			states.States = make(map[string]State, len(impl.States.States))
			for name, state := range impl.States.States {
				state.Node = WithoutNodeStatus(state.Node)
				states.States[name] = state
			}
			impl.States = &states
		}
	}
	return result
}
