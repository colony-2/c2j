package recipe

import (
	"fmt"

	"github.com/colony-2/c2j/pkg/execution"
	yamlv3 "gopkg.in/yaml.v3"
)

type RecipeImpl interface {
	isRecipe()
	GetMetadata() RecipeMetadata
}

type Recipe struct {
	RecipeImpl
}

type RecipeProvider interface {
	GetRecipe(name string) (*Recipe, error)
}

func (r Recipe) GetMetdata() RecipeMetadata {
	switch t := r.RecipeImpl.(type) {
	case *RecipeState:
		return t.RecipeMetadata
	case *RecipeSequence:
		return t.RecipeMetadata
	case *RecipeOp:
		return t.RecipeMetadata
	case *RecipeChildGroup:
		return t.RecipeMetadata
	default:
		panic("invalid recipe type")
	}
}

func (n *Recipe) MarshalYAML() (interface{}, error) {
	return n.RecipeImpl, nil
}

func (n *Recipe) UnmarshalYAML(node *yamlv3.Node) error {
	if err := validateSchemaVersionField(node); err != nil {
		return err
	}
	if err := validateWorkspaceField(node); err != nil {
		return err
	}
	var raw map[string]interface{}
	if err := node.Decode(&raw); err != nil {
		return err
	}

	var impl RecipeImpl
	switch {
	case raw["state"] != nil:
		impl = &RecipeState{}
	case raw["sequence"] != nil:
		impl = &RecipeSequence{}
	case raw["op"] != nil:
		impl = &RecipeOp{}
	case raw["child_group"] != nil:
		impl = &RecipeChildGroup{}
	default:
		// update to include line/col

		return fmt.Errorf("root node must either be an op, sequence, state, or child_group at %d:%d", node.Line, node.Column)
	}

	// Second pass: decode into concrete type
	if err := node.Decode(impl); err != nil {
		return err
	}
	if err := decodeGitProfile(node, impl); err != nil {
		return err
	}
	if err := decodePerms(node, impl); err != nil {
		return err
	}

	if op, ok := impl.(*RecipeOp); ok {
		err := checkOpInputs(op.Op, op.Inputs, node.Line, node.Column)
		if err != nil {
			return err
		}
	}

	n.RecipeImpl = impl
	if err := n.GetMetadata().SourceHooks.Validate(); err != nil {
		return err
	}
	_, err := CompileInputSchema(n.GetMetdata().SchemaVersion, n.GetMetdata().InputSchema)
	if err != nil {
		return err
	}
	return nil
}

type RecipeMetadata struct {
	SchemaVersion int                     `yaml:"schema_version,omitempty" json:"schema_version,omitempty" jsonschema:"enum=1,enum=2"`
	SourceHooks   HookSource              `yaml:"source_hooks,omitempty" json:"source_hooks,omitempty" jsonschema:"enum=from_base,enum=from_base_with_rebase,enum=from_current"`
	Execution     *execution.Requirements `yaml:"execution,omitempty" json:"execution,omitempty"`
	Version       string                  `yaml:"version"`
	NodeMetadata  `yaml:",inline"`
	Defs          map[string]Node        `yaml:"defs,omitempty"`         // Shared node definitions
	InputSchema   map[string]InputSchema `yaml:"input_schema,omitempty"` // Optional schema for inputs
	Extensions    ExtensionImports       `yaml:"extensions,omitempty"`
}

// HookSource selects code for cell:// hooks, independently of their workspace.
type HookSource string

const (
	HooksFromBase           HookSource = "from_base"
	HooksFromBaseWithRebase HookSource = "from_base_with_rebase"
	HooksFromCurrent        HookSource = "from_current"
)

func (s HookSource) Validate() error {
	switch s {
	case "", HooksFromBase, HooksFromBaseWithRebase, HooksFromCurrent:
		return nil
	default:
		return fmt.Errorf("invalid source_hooks %q: expected from_base, from_base_with_rebase, or from_current", s)
	}
}

type ExtensionImports struct {
	Functions []ExtensionFunctionImport `yaml:"functions,omitempty"`
}

type ExtensionFunctionImport struct {
	Selector string            `yaml:"selector"`
	Include  []string          `yaml:"include,omitempty"`
	Rename   map[string]string `yaml:"rename,omitempty"`
}

func (n RecipeMetadata) ValidateInputShapeAndFillDefaults(data map[string]interface{}) (map[string]interface{}, error) {
	contract, err := CompileInputSchema(n.SchemaVersion, n.InputSchema)
	if err != nil {
		return nil, err
	}
	return contract.Apply(data)
}

type RecipeSequence struct {
	RecipeMetadata `yaml:",inline" refer:"true"`
	SequenceData   `yaml:",inline" refer:"true"`
}

func (r *RecipeSequence) GetMetadata() RecipeMetadata {
	return r.RecipeMetadata
}

func (r *RecipeSequence) isRecipe() {}

type RecipeState struct {
	RecipeMetadata   `yaml:",inline" refer:"true"`
	StateMachineData `yaml:",inline" refer:"true"`
}

func (r *RecipeState) GetMetadata() RecipeMetadata {
	return r.RecipeMetadata
}

func (r *RecipeState) isRecipe() {}

type RecipeOp struct {
	RecipeMetadata `yaml:",inline" refer:"true"`
	OpData         `yaml:",inline" refer:"true"`
}

func (r *RecipeOp) GetMetadata() RecipeMetadata {
	return r.RecipeMetadata
}

func (r *RecipeOp) isRecipe() {}
