package recipe

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/colony-2/c2j/pkg/core/cel"
	"github.com/colony-2/c2j/pkg/core/ops"
	"github.com/invopop/jsonschema"
	yamlv3 "gopkg.in/yaml.v3"
)

type Node struct {
	NodeImpl
}

func (n Node) GetMetadata() NodeMetadata {
	return n.NodeImpl.GetMetadata()
}

// MarshalYAML ensures Node marshals to the same shape expected by UnmarshalYAML
// by delegating directly to the underlying concrete implementation.
func (n Node) MarshalYAML() (interface{}, error) {
	return n.NodeImpl, nil
}

func (Node) JSONSchema() *jsonschema.Schema {
	s := &jsonschema.Schema{}
	s.Ref = "#/$defs/Node"
	return s
}

func (n *Node) UnmarshalYAML(node *yamlv3.Node) error {
	var raw map[string]interface{}
	if err := node.Decode(&raw); err != nil {
		return err
	}

	var nodeOp *NodeOp
	var impl NodeImpl
	switch {
	case raw["state"] != nil:
		impl = &NodeState{}
	case raw["sequence"] != nil:
		impl = &NodeSequence{}
	case raw["op"] != nil:
		nodeOp = &NodeOp{}
		impl = nodeOp
	case raw["shared"] != nil:
		impl = &NodeShared{}
	default:
		foo, _ := yamlv3.Marshal(node)
		return fmt.Errorf("intermediate node must either be a op, sequence, state, or shared reference at %d:%d. tree: %s", node.Line, node.Column, foo)
	}

	// Second pass: decode into concrete type
	if err := node.Decode(impl); err != nil {
		return err
	}

	if nodeOp != nil {
		if err := checkOpInputs(nodeOp.Op, nodeOp.Inputs, node.Line, node.Column); err != nil {
			return err
		}
	}
	n.NodeImpl = impl
	return nil
}

func checkOpInputs(opName string, inputs map[string]interface{}, line int, col int) error {
	op, exists := ops.Get(opName)
	if !exists {
		if isSelectorOp(opName) {
			return nil
		}
		return fmt.Errorf("unknown op: [%s] at [%d:%d]", opName, line, col)
	}
	// Unmarshal into a pointer to the concrete input value so that any
	// type-provided UnmarshalYAML (e.g., validation wrappers) is invoked.
	chain := op.TaskChain()
	if len(chain) == 0 {
		return fmt.Errorf("op %s has no task steps", opName)
	}
	inT := chain[0].InputType
	var dest interface{}
	if inT.Kind() == reflect.Pointer {
		dest = reflect.New(inT.Elem()).Interface()
	} else {
		dest = reflect.New(inT).Interface()
	}

	data, err := yamlv3.Marshal(inputs)
	if err != nil {
		return err
	}
	if err := yamlv3.Unmarshal(data, dest); err != nil {
		if containsTemplateValue(inputs) {
			// Allow template expressions to defer type checks until runtime.
			return nil
		}
		return fmt.Errorf("invalid inputs for op [%s] at [%d:%d]: %w", opName, line, col, err)
	}
	return nil
}

func containsTemplateValue(value interface{}) bool {
	switch v := value.(type) {
	case string:
		return isTemplateString(v)
	case map[string]interface{}:
		for _, item := range v {
			if containsTemplateValue(item) {
				return true
			}
		}
	case map[interface{}]interface{}:
		for _, item := range v {
			if containsTemplateValue(item) {
				return true
			}
		}
	case []interface{}:
		for _, item := range v {
			if containsTemplateValue(item) {
				return true
			}
		}
	}
	return false
}

func isTemplateString(value string) bool {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "${{") && strings.HasSuffix(trimmed, "}}") {
		return true
	}
	return strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}")
}

type NodeImpl interface {
	isNode()
	GetMetadata() NodeMetadata
}

type NodeList []Node

type NodeShared struct {
	Shared string `yaml:"shared"`
}

func (n *NodeShared) GetMetadata() NodeMetadata {
	panic("not supported for shared. should have been replaced already")
}

func (n *NodeShared) isNode() {}

type NodeSequence struct {
	NodeMetadata `yaml:",inline"`
	SequenceData `yaml:",inline"`
}

func (n *NodeSequence) GetMetadata() NodeMetadata {
	return n.NodeMetadata
}

func (n *NodeSequence) isNode() {}

type NodeState struct {
	NodeMetadata     `yaml:",inline"`
	StateMachineData `yaml:",inline"`
}

func (n *NodeState) GetMetadata() NodeMetadata {
	return n.NodeMetadata
}

func (n *NodeState) isNode() {}

type NodeOp struct {
	NodeMetadata `yaml:",inline"`
	OpData       `yaml:",inline"`
}

func (n *NodeOp) GetMetadata() NodeMetadata {
	return n.NodeMetadata
}

func (NodeOp) JSONSchema() *jsonschema.Schema {
	opList := ops.List()
	items := make([]interface{}, 0, len(opList))
	for _, op := range opList {
		chain := op.TaskChain()
		if len(chain) == 0 {
			continue
		}
		inT := chain[0].InputType
		var inputStruct interface{}
		if inT.Kind() == reflect.Pointer {
			inputStruct = reflect.New(inT.Elem()).Interface()
		} else {
			inputStruct = reflect.New(inT).Elem().Interface()
		}
		items = append(items, inputStruct)
	}
	schema := oneOfSchema("op", items...)
	selectorProperties := jsonschema.NewProperties()
	selectorProperties.Set("op", &jsonschema.Schema{
		Type:    "string",
		Pattern: `^(git\+.+|\./.+|\.\./.+)$`,
	})
	selectorProperties.Set("inputs", &jsonschema.Schema{
		Type:                 "object",
		AdditionalProperties: &jsonschema.Schema{},
	})
	schema.OneOf = append(schema.OneOf, &jsonschema.Schema{
		Type:       "object",
		Properties: selectorProperties,
		Required:   []string{"op"},
		Title:      "selector-op",
	})
	return schema
}

func (n *NodeOp) isNode() {}

type NodeMetadata struct {
	ID        string                 `yaml:"id,omitempty"`
	Desc      string                 `yaml:"desc,omitempty"`
	Const     bool                   `yaml:"const,omitempty"`
	Timeout   Duration               `yaml:"timeout,omitempty"`
	Retry     *RetryPolicy           `yaml:"retry,omitempty"`
	Inputs    map[string]interface{} `yaml:"inputs,omitempty"`
	Artifacts map[string]interface{} `yaml:"artifacts,omitempty"`
	When      cel.CELExpr            `yaml:"when,omitempty"` // Conditional execution
}
