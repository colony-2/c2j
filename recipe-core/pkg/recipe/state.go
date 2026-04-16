package recipe

import (
	"fmt"
	"strings"

	"github.com/colony-2/c2j/recipe-core/pkg/cel"
	"github.com/invopop/jsonschema"
	yamlv3 "gopkg.in/yaml.v3"
)

// StateMap represents a state machine configuration
type StateMap struct {
	Initial InitialTransitions `yaml:"initial" validate:"required"` // State selector transitions (string is supported as shorthand)
	States  map[string]State   `yaml:"states"`                      // Inline the states
}

// InitialState returns the shorthand initial transition to a named state.
func InitialState(stateName string) InitialTransitions {
	transition, err := alwaysTrueTransition(stateName)
	if err != nil {
		// "true" is a valid CEL literal; this should never happen.
		panic(err)
	}
	return InitialTransitions{transition}
}

// InitialTransitions represents the transition block used to choose the first state.
// YAML supports:
//   - `initial: state_name` (shorthand)
//   - `initial: {to: state_name, when: ...}`
//   - `initial: [{to: state_a, when: ...}, ...]`
type InitialTransitions []Transition

func (i InitialTransitions) Transitions() []Transition {
	return []Transition(i)
}

func (i InitialTransitions) ShortcutState() (string, bool) {
	if len(i) != 1 || !i[0].When.AlwaysTrue() {
		return "", false
	}
	return i[0].To, true
}

func (i InitialTransitions) MarshalYAML() (interface{}, error) {
	if name, ok := i.ShortcutState(); ok {
		return name, nil
	}
	if len(i) == 1 {
		return i[0], nil
	}
	return []Transition(i), nil
}

func (i *InitialTransitions) UnmarshalYAML(node *yamlv3.Node) error {
	switch node.Kind {
	case yamlv3.ScalarNode:
		var stateName string
		if err := node.Decode(&stateName); err != nil {
			return err
		}
		transition, err := alwaysTrueTransition(stateName)
		if err != nil {
			return err
		}
		*i = InitialTransitions{transition}
		return nil
	case yamlv3.MappingNode:
		var transition Transition
		if err := node.Decode(&transition); err != nil {
			return err
		}
		*i = InitialTransitions{transition}
		return nil
	case yamlv3.SequenceNode:
		var transitions []Transition
		if err := node.Decode(&transitions); err != nil {
			return err
		}
		*i = InitialTransitions(transitions)
		return nil
	default:
		return fmt.Errorf("initial must be a state name string, transition object, or transition list")
	}
}

func (InitialTransitions) JSONSchema() *jsonschema.Schema {
	transition := (Transition{}).JSONSchema()
	minItems := uint64(1)
	return &jsonschema.Schema{
		OneOf: []*jsonschema.Schema{
			{Type: "string"},
			transition,
			{
				Type:     "array",
				Items:    transition,
				MinItems: &minItems,
			},
		},
	}
}

// State represents a state in a state machine
// A state is a node plus transition information
type State struct {
	Node                `yaml:",inline"`
	SingleStateMetadata `yaml:",inline"`
}

type SingleStateMetadata struct {
	Transitions []Transition `yaml:"transitions,omitempty" json:"transitions,omitempty"`
}

// MarshalYAML customizes encoding so state-only fields (like transitions) are preserved
// alongside the inline node fields.
func (s State) MarshalYAML() (interface{}, error) {
	// Use the node's MarshalYAML behavior (delegates to NodeImpl) and then merge in
	// state-only keys.
	var out map[string]interface{}
	b, err := yamlv3.Marshal(s.Node)
	if err != nil {
		return nil, err
	}
	if err := yamlv3.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if len(s.Transitions) > 0 {
		out["transitions"] = s.Transitions
	}
	return out, nil
}

// UnmarshalYAML customizes decoding so state-only fields (like transitions) don't get
// swallowed by the inline Node's UnmarshalYAML implementation.
func (s *State) UnmarshalYAML(node *yamlv3.Node) error {
	// Decode transitions (ignore unknown keys here; strictness is enforced at the recipe level).
	type metaOnly struct {
		Transitions []Transition `yaml:"transitions,omitempty"`
	}
	var meta metaOnly
	if err := node.Decode(&meta); err != nil {
		return err
	}

	// Decode the node definition using a filtered mapping node that omits state-only keys.
	filtered := &yamlv3.Node{
		Kind:    yamlv3.MappingNode,
		Tag:     "!!map",
		Content: make([]*yamlv3.Node, 0, len(node.Content)),
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		k := node.Content[i]
		v := node.Content[i+1]
		if k.Kind == yamlv3.ScalarNode && k.Value == "transitions" {
			continue
		}
		filtered.Content = append(filtered.Content, k, v)
	}

	var n Node
	if err := filtered.Decode(&n); err != nil {
		return err
	}

	s.Node = n
	s.SingleStateMetadata = SingleStateMetadata{Transitions: meta.Transitions}
	return nil
}

// Transition represents a state transition
type Transition struct {
	To   string      `yaml:"to"`
	When cel.CELExpr `yaml:"when,omitempty"` // CEL expression
}

func (Transition) JSONSchema() *jsonschema.Schema {
	when := &jsonschema.Schema{
		OneOf: []*jsonschema.Schema{
			{Type: "string"},
			{Type: "boolean"},
		},
	}
	props := jsonschema.NewProperties()
	props.Set("to", &jsonschema.Schema{Type: "string"})
	props.Set("when", when)
	return &jsonschema.Schema{
		Type:       "object",
		Properties: props,
		Required:   []string{"to"},
	}
}

func (t *Transition) UnmarshalYAML(node *yamlv3.Node) error {
	type rawTransition struct {
		To   string      `yaml:"to"`
		When interface{} `yaml:"when,omitempty"`
	}
	var raw rawTransition
	if err := node.Decode(&raw); err != nil {
		return err
	}

	exprText, err := normalizeTransitionWhen(raw.When)
	if err != nil {
		return err
	}
	expr, err := celExprFromString(exprText)
	if err != nil {
		return err
	}
	t.To = raw.To
	t.When = expr
	return nil
}

func normalizeTransitionWhen(value interface{}) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	default:
		return "", fmt.Errorf("transition 'when' must be a CEL string or boolean")
	}
}

func alwaysTrueTransition(stateName string) (Transition, error) {
	if strings.TrimSpace(stateName) == "" {
		return Transition{}, fmt.Errorf("state name is required")
	}
	expr, err := celExprFromString("true")
	if err != nil {
		return Transition{}, err
	}
	return Transition{
		To:   stateName,
		When: expr,
	}, nil
}

func celExprFromString(exprText string) (cel.CELExpr, error) {
	var expr cel.CELExpr
	if err := expr.UnmarshalYAML(func(target interface{}) error {
		strPtr, ok := target.(*string)
		if !ok {
			return fmt.Errorf("internal error: CELExpr unmarshal target must be *string")
		}
		*strPtr = exprText
		return nil
	}); err != nil {
		return cel.CELExpr{}, err
	}
	return expr, nil
}

type StateMachineData struct {
	States  *StateMap              `yaml:"state,omitempty"`
	Outputs map[string]interface{} `yaml:"outputs,omitempty"`
}
