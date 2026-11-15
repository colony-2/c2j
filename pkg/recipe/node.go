package recipe

import (
	"fmt"
	"github.com/colony-2/c2j/pkg/execution"
	"reflect"
	"strings"

	"github.com/colony-2/c2j/pkg/cel"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
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
	if err := validateWorkspaceField(node); err != nil {
		return err
	}
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
	case raw["child_group"] != nil:
		impl = &NodeChildGroup{}
	case raw["shared"] != nil:
		impl = &NodeShared{}
	case raw["include"] != nil:
		impl = &NodeInclude{}
	default:
		foo, _ := yamlv3.Marshal(node)
		return fmt.Errorf("intermediate node must either be a op, sequence, state, include, shared reference, or a child_group at %d:%d. tree: %s", node.Line, node.Column, foo)
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

type IncludeData struct {
	Recipe    string           `yaml:"recipe" json:"recipe"`
	Bind      string           `yaml:"bind,omitempty" json:"bind,omitempty"`
	IfMissing *IncludeFallback `yaml:"if_missing,omitempty" json:"if_missing,omitempty"`
}

type IncludeFallback struct {
	Outputs map[string]interface{} `yaml:"outputs" json:"outputs"`
}

// JSONSchema accepts both include authoring forms, including optional hooks.
func (IncludeData) JSONSchema() *jsonschema.Schema {
	fallback := jsonschema.NewProperties()
	fallback.Set("outputs", &jsonschema.Schema{Type: "object", AdditionalProperties: &jsonschema.Schema{}})
	fields := jsonschema.NewProperties()
	fields.Set("recipe", &jsonschema.Schema{Type: "string"})
	fields.Set("bind", &jsonschema.Schema{Type: "string", Enum: []interface{}{"late"}})
	fields.Set("if_missing", &jsonschema.Schema{Type: "object", Properties: fallback, Required: []string{"outputs"}, AdditionalProperties: jsonschema.FalseSchema})
	return &jsonschema.Schema{OneOf: []*jsonschema.Schema{
		{Type: "string"},
		{Type: "object", Properties: fields, Required: []string{"recipe"}, AdditionalProperties: jsonschema.FalseSchema},
	}}
}

func (i *IncludeData) UnmarshalYAML(node *yamlv3.Node) error {
	switch node.Kind {
	case yamlv3.ScalarNode:
		var recipeRef string
		if err := node.Decode(&recipeRef); err != nil {
			return err
		}
		i.Recipe = strings.TrimSpace(recipeRef)
		return nil
	case yamlv3.MappingNode:
		seen := map[string]bool{}
		for j := 0; j < len(node.Content); j += 2 {
			key, value := node.Content[j].Value, node.Content[j+1]
			if seen[key] {
				return fmt.Errorf("duplicate include field %q", key)
			}
			seen[key] = true
			if key != "recipe" && key != "if_missing" && key != "bind" {
				return fmt.Errorf("unknown include field %q", key)
			}
			if key == "bind" && (value.Kind != yamlv3.ScalarNode || value.Tag != "!!str" || value.Value != "late") {
				return fmt.Errorf("include.bind must be the literal string late; omit bind for eager includes")
			}
			if key == "if_missing" {
				if value.Kind != yamlv3.MappingNode || len(value.Content) != 2 || value.Content[0].Value != "outputs" || value.Content[1].Kind != yamlv3.MappingNode {
					return fmt.Errorf("include.if_missing requires an outputs object")
				}
			}
		}
		if seen["bind"] {
			for j := 0; j < len(node.Content); j += 2 {
				value := node.Content[j+1]
				if node.Content[j].Value == "recipe" && (value.Kind != yamlv3.ScalarNode || value.Tag != "!!str" || strings.TrimSpace(value.Value) == "") {
					return fmt.Errorf("include.recipe must be a non-empty string")
				}
			}
		}
		if seen["bind"] && !seen["recipe"] {
			return fmt.Errorf("late include requires include.recipe")
		}
		type includeData IncludeData
		var data includeData
		if err := node.Decode(&data); err != nil {
			return err
		}
		data.Recipe = strings.TrimSpace(data.Recipe)
		if data.Bind == "late" && (data.IfMissing != nil || strings.HasPrefix(data.Recipe, "cell://")) {
			return fmt.Errorf("late include cannot use cell hooks or if_missing")
		}
		*i = IncludeData(data)
		return nil
	default:
		return fmt.Errorf("include must be a recipe reference string or object")
	}
}

type NodeInclude struct {
	NodeMetadata `yaml:",inline"`
	Include      IncludeData `yaml:"include" json:"include"`
}

func (n *NodeInclude) GetMetadata() NodeMetadata {
	return n.NodeMetadata
}

func (n *NodeInclude) isNode() {}

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
	ExpectOutputs  map[string]interface{} `yaml:"expect_outputs,omitempty" json:"expect_outputs,omitempty"`
	Perms          *permissions.Spec      `yaml:"perms,omitempty" json:"perms,omitempty"`
	GitProfile     *GitProfileSpec        `yaml:"git_profile,omitempty" json:"git_profile,omitempty"`
	Workspace      *WorkspaceSpec         `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	ExecutionNeeds *ExecutionNeeds        `yaml:"execution_needs,omitempty" json:"execution_needs,omitempty"`
	ID             string                 `yaml:"id,omitempty"`
	Desc           string                 `yaml:"desc,omitempty"`
	Const          bool                   `yaml:"const,omitempty"`
	Timeout        Duration               `yaml:"timeout,omitempty"`
	Retry          *RetryPolicy           `yaml:"retry,omitempty"`
	Inputs         map[string]interface{} `yaml:"inputs,omitempty"`
	Vars           map[string]interface{} `yaml:"vars,omitempty"`
	Artifacts      map[string]interface{} `yaml:"artifacts,omitempty"`
	When           cel.CELExpr            `yaml:"when,omitempty"` // Conditional execution
	Catch          []CatchClause          `yaml:"catch,omitempty" json:"catch,omitempty"`
	Internal       *NodeInternalMetadata  `yaml:"__c2j_internal,omitempty" json:"__c2j_internal,omitempty" jsonschema:"-"`
}

type NodeInternalMetadata struct {
	BoundInputs      map[string]interface{}    `yaml:"-" json:"-" jsonschema:"-"`
	FunctionPackages []FunctionPackageSnapshot `yaml:"function_packages,omitempty" json:"function_packages,omitempty"`
	DefinitionSource *RecipeSourceSnapshot     `yaml:"definition_source,omitempty" json:"definition_source,omitempty"`
	Late             *LateIncludeMetadata      `yaml:"late,omitempty" json:"late,omitempty"`
	// CELProvider is installed at runtime when a deferred include is loaded.
	CELProvider    cel.OptionsProvider `yaml:"-" json:"-" jsonschema:"-"`
	HookSourceRepo string              `yaml:"hook_source_repo,omitempty" json:"hook_source_repo,omitempty"`
	HookSourceRef  string              `yaml:"hook_source_ref,omitempty" json:"hook_source_ref,omitempty"`
	// HookAuthority is installed only by the controller at invocation entry.
	// It must never be accepted from even an internal recipe artifact.
	HookAuthority          *permissions.State       `yaml:"-" json:"-" jsonschema:"-"`
	RootRepository         *string                  `yaml:"root_repository,omitempty" json:"root_repository,omitempty"`
	Inline                 *InlineInclusionMetadata `yaml:"inline,omitempty" json:"inline,omitempty"`
	CompositeSchemaVersion int                      `yaml:"composite_schema_version,omitempty" json:"composite_schema_version,omitempty"`
	CompositeInputSchema   map[string]InputSchema   `yaml:"composite_input_schema,omitempty" json:"composite_input_schema,omitempty"`
}

// FunctionPackageSnapshot keeps deferred CEL imports usable when the recipe
// source is unavailable. Files are base64 encoded, relative to the Git source
// root. This is compiler-only data inside the definition artifact.
type FunctionPackageSnapshot struct {
	Selector         string            `yaml:"selector" json:"selector"`
	ResolvedSelector string            `yaml:"resolved_selector" json:"resolved_selector"`
	ResolvedCommit   string            `yaml:"resolved_commit" json:"resolved_commit"`
	PackageDir       string            `yaml:"package_dir" json:"package_dir"`
	Files            map[string]string `yaml:"files_base64" json:"files_base64"`
	FileModes        map[string]uint32 `yaml:"file_modes" json:"file_modes"`
	Symlinks         map[string]string `yaml:"symlinks,omitempty" json:"symlinks,omitempty"`
}

// LateIncludeMetadata belongs to the declaring source, never to the active
// workspace or selected recipe. It survives internal definition capture.
type LateIncludeMetadata struct {
	DeclaringSource     RecipeSourceSnapshot `yaml:"declaring_source" json:"declaring_source"`
	DeclaringRepository string               `yaml:"declaring_repository" json:"declaring_repository"`
	RootRepository      string               `yaml:"root_repository" json:"root_repository"`
	CallsitePath        string               `yaml:"callsite_path" json:"callsite_path"`
}

type InlineInclusionMetadata struct {
	TrustedSource      bool                    `yaml:"trusted_source,omitempty" json:"trusted_source,omitempty"`
	PermissionBoundary bool                    `yaml:"permission_boundary,omitempty" json:"permission_boundary,omitempty"`
	Execution          *execution.Requirements `yaml:"execution,omitempty" json:"execution,omitempty"`
	CallsitePath       string                  `yaml:"callsite_path,omitempty" json:"callsite_path,omitempty"`
	RecipeID           string                  `yaml:"recipe_id,omitempty" json:"recipe_id,omitempty"`
	RecipeVersion      string                  `yaml:"recipe_version,omitempty" json:"recipe_version,omitempty"`
	Source             RecipeSourceSnapshot    `yaml:"source,omitempty" json:"source,omitempty"`
	ContentSHA256      string                  `yaml:"content_sha256,omitempty" json:"content_sha256,omitempty"`
	ResolvedSelectors  map[string]string       `yaml:"resolved_selectors,omitempty" json:"resolved_selectors,omitempty"`
}

type RecipeSourceSnapshot struct {
	SourceKind        string `yaml:"source_kind,omitempty" json:"source_kind,omitempty"`
	SubmittedSelector string `yaml:"submitted_selector,omitempty" json:"submitted_selector,omitempty"`
	ResolvedSelector  string `yaml:"resolved_selector,omitempty" json:"resolved_selector,omitempty"`
	ResolvedCommit    string `yaml:"resolved_commit,omitempty" json:"resolved_commit,omitempty"`
	ArtifactName      string `yaml:"artifact_name,omitempty" json:"artifact_name,omitempty"`
	WasAlreadyPinned  bool   `yaml:"was_already_pinned,omitempty" json:"was_already_pinned,omitempty"`
}
