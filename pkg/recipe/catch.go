package recipe

import (
	"fmt"
	"strings"

	"github.com/colony-2/c2j/pkg/cel"
	"github.com/invopop/jsonschema"
	orderedmap "github.com/wk8/go-ordered-map/v2"
	yamlv3 "gopkg.in/yaml.v3"
)

type CatchClause struct {
	ID       string                 `yaml:"id,omitempty" json:"id,omitempty"`
	When     cel.CELExpr            `yaml:"when,omitempty" json:"when,omitempty"`
	To       string                 `yaml:"to,omitempty" json:"to,omitempty"`
	Payload  map[string]interface{} `yaml:"payload,omitempty" json:"payload,omitempty"`
	Continue *CatchContinue         `yaml:"continue,omitempty" json:"continue,omitempty"`
	Fail     *CatchFail             `yaml:"fail,omitempty" json:"fail,omitempty"`
}

type CatchContinue struct {
	Outputs map[string]interface{} `yaml:"outputs,omitempty" json:"outputs,omitempty"`
}

type CatchFail struct {
	Kind    string      `yaml:"kind,omitempty" json:"kind,omitempty"`
	Code    string      `yaml:"code,omitempty" json:"code,omitempty"`
	Message string      `yaml:"message,omitempty" json:"message,omitempty"`
	Cause   interface{} `yaml:"cause,omitempty" json:"cause,omitempty"`
}

func (c *CatchClause) UnmarshalYAML(node *yamlv3.Node) error {
	if node.Kind != yamlv3.MappingNode {
		return fmt.Errorf("catch clause must be an object")
	}

	type rawCatch struct {
		ID       string                 `yaml:"id,omitempty"`
		To       string                 `yaml:"to,omitempty"`
		Payload  map[string]interface{} `yaml:"payload,omitempty"`
		Continue *CatchContinue         `yaml:"continue,omitempty"`
		Fail     *CatchFail             `yaml:"fail,omitempty"`
	}

	var raw rawCatch
	var when cel.CELExpr
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valueNode := node.Content[i+1]
		if keyNode.Kind != yamlv3.ScalarNode {
			return fmt.Errorf("catch clause keys must be strings")
		}
		key := keyNode.Value
		if seen[key] {
			return fmt.Errorf("duplicate catch field %q", key)
		}
		seen[key] = true

		switch key {
		case "id", "to", "payload", "continue", "fail":
			continue
		case "when":
			decoded, err := decodeCatchWhen(valueNode)
			if err != nil {
				return err
			}
			when = decoded
		default:
			return fmt.Errorf("unknown catch field %q", key)
		}
	}

	if err := node.Decode(&raw); err != nil {
		return err
	}
	if raw.Fail != nil {
		if err := validateCatchFailNode(node); err != nil {
			return err
		}
	}

	*c = CatchClause{
		ID:       raw.ID,
		When:     when,
		To:       raw.To,
		Payload:  raw.Payload,
		Continue: raw.Continue,
		Fail:     raw.Fail,
	}
	return c.Validate()
}

func decodeCatchWhen(node *yamlv3.Node) (cel.CELExpr, error) {
	switch node.Kind {
	case yamlv3.ScalarNode:
		if node.Tag == "!!bool" {
			var b bool
			if err := node.Decode(&b); err != nil {
				return cel.CELExpr{}, err
			}
			if b {
				return celExprFromString("")
			}
			return celExprFromString("false")
		}
		if node.Tag == "!!str" || node.Tag == "" {
			var s string
			if err := node.Decode(&s); err != nil {
				return cel.CELExpr{}, err
			}
			return celExprFromString(s)
		}
	}
	return cel.CELExpr{}, fmt.Errorf("catch.when must be a string or boolean")
}

func validateCatchFailNode(node *yamlv3.Node) error {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != "fail" {
			continue
		}
		failNode := node.Content[i+1]
		if failNode.Kind != yamlv3.MappingNode {
			return fmt.Errorf("catch.fail must be an object")
		}
		for j := 0; j+1 < len(failNode.Content); j += 2 {
			key := failNode.Content[j].Value
			switch key {
			case "kind", "code", "message", "cause":
			default:
				return fmt.Errorf("unknown catch.fail field %q", key)
			}
		}
	}
	return nil
}

func (c CatchClause) Validate() error {
	actions := 0
	if strings.TrimSpace(c.To) != "" {
		actions++
	}
	if c.Continue != nil {
		actions++
	}
	if c.Fail != nil {
		actions++
	}
	if actions != 1 {
		return fmt.Errorf("catch clause must specify exactly one action: to, continue, or fail")
	}
	if len(c.Payload) > 0 && strings.TrimSpace(c.To) == "" {
		return fmt.Errorf("catch payload is only valid with to")
	}
	return nil
}

func (CatchClause) JSONSchema() *jsonschema.Schema {
	common := func() *orderedmap.OrderedMap[string, *jsonschema.Schema] {
		props := jsonschema.NewProperties()
		props.Set("id", &jsonschema.Schema{Type: "string"})
		props.Set("when", &jsonschema.Schema{OneOf: []*jsonschema.Schema{
			{Type: "string"},
			{Type: "boolean"},
		}})
		return props
	}
	route := &jsonschema.Schema{
		Type:                 "object",
		Properties:           common(),
		Required:             []string{"to"},
		AdditionalProperties: jsonschema.FalseSchema,
	}
	route.Properties.Set("to", &jsonschema.Schema{Type: "string"})
	route.Properties.Set("payload", objectSchema())

	cont := &jsonschema.Schema{
		Type:                 "object",
		Properties:           common(),
		Required:             []string{"continue"},
		AdditionalProperties: jsonschema.FalseSchema,
	}
	continueSchema := &jsonschema.Schema{
		Type:                 "object",
		Properties:           jsonschema.NewProperties(),
		AdditionalProperties: jsonschema.FalseSchema,
	}
	continueSchema.Properties.Set("outputs", objectSchema())
	cont.Properties.Set("continue", continueSchema)

	failProps := jsonschema.NewProperties()
	failProps.Set("kind", &jsonschema.Schema{Type: "string"})
	failProps.Set("code", &jsonschema.Schema{Type: "string"})
	failProps.Set("message", &jsonschema.Schema{Type: "string"})
	failProps.Set("cause", &jsonschema.Schema{})
	fail := &jsonschema.Schema{
		Type:                 "object",
		Properties:           common(),
		Required:             []string{"fail"},
		AdditionalProperties: jsonschema.FalseSchema,
	}
	fail.Properties.Set("fail", &jsonschema.Schema{
		Type:                 "object",
		Properties:           failProps,
		AdditionalProperties: jsonschema.FalseSchema,
	})

	return &jsonschema.Schema{
		Title: "CatchClause",
		OneOf: []*jsonschema.Schema{route, cont, fail},
	}
}

func objectSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:                 "object",
		AdditionalProperties: &jsonschema.Schema{},
	}
}
