package recipe

import (
	"encoding/json"
	"fmt"
	"log"
	"reflect"

	"github.com/colony-2/c2j/pkg/core/ops"
	"github.com/invopop/jsonschema"
)

var sharedReflector = &jsonschema.Reflector{
	// Use yaml tag instead of json tag for field names
	FieldNameTag:               "yaml",
	AllowAdditionalProperties:  true,
	RequiredFromJSONSchemaTags: true,
	ExpandedStruct:             true,
	DoNotReference:             true,
}

func GenerateSchemaString() (string, error) {
	r := sharedReflector
	schema := r.Reflect(Recipe{})
	//schema := &jsonschema.Schema{}
	if schema.Definitions == nil {
		schema.Definitions = make(map[string]*jsonschema.Schema)
	}

	r.Anonymous = true
	nodeSchema, err := getNodeSchema(r)
	if err != nil {
		return "", err
	}
	schema.Definitions["Node"] = nodeSchema
	s, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return "", err
	}

	return string(s), nil

}

func stripSchema(s *jsonschema.Schema) *jsonschema.Schema {
	s.Version = ""
	return s
}

func getNodeSchema(r *jsonschema.Reflector) (*jsonschema.Schema, error) {
	nmd := r.Reflect(NodeMetadata{})
	opList := ops.List()
	items := make([]*jsonschema.Schema, 0, len(opList))
	seen := make(map[string]bool)
	for _, op := range opList {
		opTypeName := op.GetMetadata().Type
		if seen[opTypeName] {
			continue
		}
		seen[opTypeName] = true
		local, err := cloneSchema(nmd)
		if err != nil {
			return nil, err
		}
		opType := &jsonschema.Schema{}
		opType.Const = opTypeName
		opType.Type = "string"
		local.Properties.Set("op", opType)
		local.Type = "object"
		chain := op.TaskChain()
		if len(chain) == 0 {
			return nil, fmt.Errorf("op %q has no task steps", op.GetName())
		}
		firstStep := chain[0]
		inT := firstStep.InputType
		if inT.Kind() != reflect.Struct {
			if !(inT.Kind() == reflect.Pointer && inT.Elem().Kind() == reflect.Struct) {
				return nil, fmt.Errorf("op %q inputs must be a struct, got %s", op.GetName(), inT.String())
			}
		}
		var inputStruct interface{}
		if inT.Kind() == reflect.Pointer {
			inputStruct = reflect.New(inT.Elem()).Interface()
		} else {
			inputStruct = reflect.New(inT).Elem().Interface()
		}
		inputsSchema, err := safeReflectSchema(r, inputStruct)
		if err != nil {
			inputsSchema = &jsonschema.Schema{
				Type:                 "object",
				AdditionalProperties: &jsonschema.Schema{},
			}
		}
		inputsSchema = stripSchema(inputsSchema)

		// Tighten required fields for certain well-known ops without changing unmarshalling
		switch opTypeName {
		case "command_execution":
			// Require 'run' when inputs are provided
			if inputsSchema == nil {
				inputsSchema = &jsonschema.Schema{Type: "object"}
			}
			inputsSchema.Required = append(inputsSchema.Required, "run")

		case "recipe":
			// Require config.recipe (worker's recipe-invocation op uses nested config.recipe)
			cfg := &jsonschema.Schema{Type: "object"}
			cfg.Properties = jsonschema.NewProperties()
			cfg.Properties.Set("recipe", &jsonschema.Schema{Type: "string"})
			cfg.Required = append(cfg.Required, "recipe")

			is := &jsonschema.Schema{Type: "object"}
			is.Properties = jsonschema.NewProperties()
			is.Properties.Set("config", cfg)
			is.Required = append(is.Required, "config")
			inputsSchema = is
		}

		local.Properties.Set("inputs", inputsSchema)
		// 'inputs' are optional at YAML level; only 'op' is required
		local.Required = append(local.Required, "op")
		local.Title = op.GetName()
		items = append(items, local)
	}
	selectorNode, err := cloneSchema(nmd)
	if err != nil {
		return nil, err
	}
	selectorNode.Type = "object"
	selectorNode.Properties.Set("op", &jsonschema.Schema{
		Type:    "string",
		Pattern: `^(git\+.+|\./.+|\.\./.+)$`,
	})
	selectorNode.Properties.Set("inputs", &jsonschema.Schema{
		Type:                 "object",
		AdditionalProperties: &jsonschema.Schema{},
	})
	selectorNode.Required = append(selectorNode.Required, "op")
	selectorNode.Title = "selector-op"
	items = append(items, selectorNode)
	seq := stripSchema(r.Reflect(NodeSequence{}))
	seq.Title = "Sequence"
	// Require presence of the sequence key to avoid oneOf ambiguity
	seq.Required = append(seq.Required, "sequence")
	items = append(items, seq)
	state := stripSchema(r.Reflect(NodeState{}))
	state.Title = "State"
	// Require presence of the state key to avoid oneOf ambiguity
	state.Required = append(state.Required, "state")
	items = append(items, state)

	nodeopSchema := &jsonschema.Schema{
		Title: "Node",
		OneOf: items,
	}
	return nodeopSchema, nil
}

func safeReflectSchema(reflector *jsonschema.Reflector, value interface{}) (schema *jsonschema.Schema, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("schema reflect panic: %v", r)
			err = fmt.Errorf("schema reflect panic: %v", r)
		}
	}()
	return reflector.Reflect(value), nil
}

func cloneSchema(src *jsonschema.Schema) (*jsonschema.Schema, error) {
	if src == nil {
		return nil, nil
	}
	b, err := json.Marshal(src) // calls (*Schema).MarshalJSON
	if err != nil {
		return nil, err
	}
	var dst jsonschema.Schema
	if err := json.Unmarshal(b, &dst); err != nil { // calls (*Schema).UnmarshalJSON
		return nil, err
	}
	return &dst, nil
}

func PrintSchema() {
	s, err := GenerateSchemaString()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(s)
}

func oneOfSchema(name string, t ...any) *jsonschema.Schema {
	schemas := make([]*jsonschema.Schema, 0, len(t))
	reflector := sharedReflector
	reflector.Anonymous = true
	for _, typ := range t {
		schemas = append(schemas, stripSchema(reflector.Reflect(typ)))
	}
	reflector.Anonymous = false
	return &jsonschema.Schema{
		OneOf: schemas,
		Title: name,
	}
}

func (Recipe) JSONSchema() *jsonschema.Schema {
	// Build a OneOf with required discriminators to avoid ambiguity
	reflector := sharedReflector
	reflector.Anonymous = true
	seq := stripSchema(reflector.Reflect(RecipeSequence{}))
	seq.Required = append(seq.Required, "sequence")
	state := stripSchema(reflector.Reflect(RecipeState{}))
	state.Required = append(state.Required, "state")
	op := stripSchema(reflector.Reflect(RecipeOp{}))
	op.Required = append(op.Required, "op")
	reflector.Anonymous = false
	return &jsonschema.Schema{Title: "recipe", OneOf: []*jsonschema.Schema{seq, state, op}}
}

//func (Node) JSONSchema() *jsonschema.Schema {
//	return oneOfSchema("node", NodeOp{}, NodeState{}, NodeShared{}, NodeSequence{})
//}
