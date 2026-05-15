package recipe

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/pkg/cel"
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
	if len(i) != 1 || !i[0].When.AlwaysTrue() || len(i[0].Payload) > 0 {
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
	var transitions []Transition

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
			decoded, err := decodeTransitionsNode(v)
			if err != nil {
				return err
			}
			transitions = decoded
			continue
		}
		filtered.Content = append(filtered.Content, k, v)
	}

	var n Node
	if err := filtered.Decode(&n); err != nil {
		return err
	}

	s.Node = n
	s.SingleStateMetadata = SingleStateMetadata{Transitions: transitions}
	return nil
}

func decodeTransitionsNode(node *yamlv3.Node) ([]Transition, error) {
	switch node.Kind {
	case yamlv3.SequenceNode:
		var transitions []Transition
		if err := node.Decode(&transitions); err != nil {
			return nil, err
		}
		return transitions, nil
	case yamlv3.MappingNode:
		var sw switchTransitions
		if err := node.Decode(&sw); err != nil {
			return nil, err
		}
		return normalizeSwitchTransitions(sw)
	case yamlv3.ScalarNode:
		if node.Tag == "!!null" {
			return nil, nil
		}
	}
	return nil, fmt.Errorf("transitions must be a transition list or switch table")
}

type switchTransitions struct {
	Switch  string            `yaml:"switch"`
	Cases   []switchCase      `yaml:"cases"`
	Default *switchCaseTarget `yaml:"default,omitempty"`
}

type switchCase struct {
	Value   interface{}            `yaml:"value"`
	When    interface{}            `yaml:"when,omitempty"`
	To      string                 `yaml:"to,omitempty"`
	Payload map[string]interface{} `yaml:"payload,omitempty"`
	Switch  *switchTransitions     `yaml:"switch,omitempty"`
}

type switchCaseTarget struct {
	To      string                 `yaml:"to,omitempty"`
	Payload map[string]interface{} `yaml:"payload,omitempty"`
}

func normalizeSwitchTransitions(sw switchTransitions) ([]Transition, error) {
	return expandSwitchTransitions(sw, "", 0)
}

func expandSwitchTransitions(sw switchTransitions, parentCond string, depth int) ([]Transition, error) {
	if strings.TrimSpace(sw.Switch) == "" {
		return nil, fmt.Errorf("switch transitions require a switch expression")
	}
	if depth > 1 {
		return nil, fmt.Errorf("nested switch transitions are limited to one nested level")
	}
	seen := map[string]struct{}{}
	transitions := []Transition{}
	fullMatches := []string{}

	for i, c := range sw.Cases {
		lit, err := switchCaseLiteral(c.Value)
		if err != nil {
			return nil, fmt.Errorf("switch case %d: %w", i, err)
		}
		key := fmt.Sprintf("%T:%s", c.Value, lit)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate switch case value %s", lit)
		}
		seen[key] = struct{}{}

		caseCond, err := switchCaseCondition(sw.Switch, lit, c.When)
		if err != nil {
			return nil, fmt.Errorf("switch case %d: %w", i, err)
		}
		caseCond = andConditions(parentCond, caseCond)

		if c.To != "" && c.Switch != nil {
			return nil, fmt.Errorf("switch case %d cannot contain both to and switch", i)
		}
		if c.To == "" && c.Switch == nil {
			return nil, fmt.Errorf("switch case %d must contain to or switch", i)
		}
		if c.To != "" {
			transitions = append(transitions, transitionFromCondition(c.To, caseCond, c.Payload))
			fullMatches = append(fullMatches, caseCond)
			continue
		}

		childTransitions, childFull, err := expandNestedSwitchTransitions(*c.Switch, caseCond, depth+1)
		if err != nil {
			return nil, fmt.Errorf("switch case %d: %w", i, err)
		}
		transitions = append(transitions, childTransitions...)
		fullMatches = append(fullMatches, childFull)
	}

	if sw.Default != nil {
		if strings.TrimSpace(sw.Default.To) == "" {
			return nil, fmt.Errorf("switch default must contain to")
		}
		defaultCond := andConditions(parentCond, notAnyCondition(fullMatches))
		transitions = append(transitions, transitionFromCondition(sw.Default.To, defaultCond, sw.Default.Payload))
	}

	return transitions, nil
}

func expandNestedSwitchTransitions(sw switchTransitions, parentCond string, depth int) ([]Transition, string, error) {
	transitions, err := expandSwitchTransitions(sw, parentCond, depth)
	if err != nil {
		return nil, "", err
	}

	childMatches := make([]string, 0, len(transitions))
	for _, transition := range transitions {
		childMatches = append(childMatches, transition.When.String())
	}
	if sw.Default != nil {
		return transitions, parentCond, nil
	}
	return transitions, orConditions(childMatches), nil
}

func switchCaseCondition(switchExpr string, literal string, rawWhen interface{}) (string, error) {
	cond := fmt.Sprintf("((%s) == %s)", switchExpr, literal)
	whenExpr, err := celExprFromRawWhen(rawWhen)
	if err != nil {
		return "", err
	}
	if whenExpr == "" || whenExpr == "true" {
		return cond, nil
	}
	if whenExpr == "false" {
		return "false", nil
	}
	return andConditions(cond, "("+whenExpr+")"), nil
}

func celExprFromRawWhen(value interface{}) (string, error) {
	return normalizeTransitionWhen(value)
}

func transitionFromCondition(to string, condition string, payload map[string]interface{}) Transition {
	expr, err := celExprFromString(condition)
	if err != nil {
		panic(err)
	}
	return Transition{To: to, When: expr, Payload: payload}
}

func andConditions(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "":
		return left
	case left == "false" || right == "false":
		return "false"
	case left == "true":
		return right
	case right == "true":
		return left
	default:
		return "(" + left + ") && (" + right + ")"
	}
}

func orConditions(conditions []string) string {
	nonEmpty := make([]string, 0, len(conditions))
	for _, cond := range conditions {
		cond = strings.TrimSpace(cond)
		if cond != "" && cond != "false" {
			nonEmpty = append(nonEmpty, "("+cond+")")
		}
	}
	if len(nonEmpty) == 0 {
		return "false"
	}
	return strings.Join(nonEmpty, " || ")
}

func notAnyCondition(conditions []string) string {
	return "!(" + orConditions(conditions) + ")"
}

func switchCaseLiteral(value interface{}) (string, error) {
	if value == nil {
		return "", fmt.Errorf("value is required")
	}
	switch v := value.(type) {
	case string:
		return strconv.Quote(v), nil
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	case int:
		return strconv.FormatInt(int64(v), 10), nil
	case int8, int16, int32, int64:
		return strconv.FormatInt(reflect.ValueOf(v).Int(), 10), nil
	case uint, uint8, uint16, uint32, uint64:
		return strconv.FormatUint(reflect.ValueOf(v).Uint(), 10), nil
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("unsupported value type %T", value)
	}
}

// Transition represents a state transition
type Transition struct {
	To      string                 `yaml:"to"`
	When    cel.CELExpr            `yaml:"when,omitempty"` // CEL expression
	Payload map[string]interface{} `yaml:"payload,omitempty"`
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
	props.Set("payload", &jsonschema.Schema{
		Type:                 "object",
		AdditionalProperties: &jsonschema.Schema{},
	})
	return &jsonschema.Schema{
		Type:       "object",
		Properties: props,
		Required:   []string{"to"},
	}
}

func (t *Transition) UnmarshalYAML(node *yamlv3.Node) error {
	type rawTransition struct {
		To      string                 `yaml:"to"`
		When    interface{}            `yaml:"when,omitempty"`
		Payload map[string]interface{} `yaml:"payload,omitempty"`
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
	t.Payload = raw.Payload
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
