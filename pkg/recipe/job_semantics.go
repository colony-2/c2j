package recipe

import (
	"fmt"
	"strings"

	"github.com/colony-2/c2j/pkg/cel"
	"github.com/colony-2/c2j/pkg/jobsemantics"
	"gopkg.in/yaml.v3"
)

// ValidateJobSemantics preflights static syntax before any recipe effects.
// Operation task contract 1 and authored recipe bytes are left unchanged.
func ValidateJobSemantics(r Recipe, version int) error {
	if err := jobsemantics.Validate(version); err != nil {
		return err
	}
	if err := ValidateJobConditions(r); err != nil {
		return fmt.Errorf("%w (job_semantics_version %d)", err, version)
	}
	raw, err := yaml.Marshal(&r)
	if err != nil {
		return err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return err
	}
	return checkJobSyntax(&document, version, syntaxNode)
}

// Track structural locations rather than guessing the grammar from a scalar's
// contents or from an application map key named "when".
type jobSyntax int

const (
	syntaxData jobSyntax = iota
	syntaxNode
	syntaxNodeMap
	syntaxState
	syntaxParallel
	syntaxCondition
	syntaxChildGroup
	syntaxChild
)

func checkJobSyntax(n *yaml.Node, version int, kind jobSyntax) error {
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "{{") && jobsemantics.CheckTemplate(n.Value) != nil {
		return fmt.Errorf("retired live goal context at line %d (job_semantics_version %d); use exact job identity", n.Line, version)
	}
	if n.Kind == yaml.MappingNode {
		var op string
		var inputs map[string]any
		for i := 0; i+1 < len(n.Content); i += 2 {
			switch n.Content[i].Value {
			case "child_group":
				if kind == syntaxNode {
					var group map[string]any
					if err := n.Content[i+1].Decode(&group); err != nil {
						return err
					}
					if err := jobsemantics.CheckOperation(version, "recipe.child_group.run_and_get_result", group, false); err != nil {
						return fmt.Errorf("line %d: %w", n.Line, err)
					}
				}
			case "op":
				op = n.Content[i+1].Value
			case "inputs":
				if err := n.Content[i+1].Decode(&inputs); err != nil {
					return err
				}
			}
		}
		if kind == syntaxNode && op != "" {
			if err := jobsemantics.CheckOperation(version, op, inputs, false); err != nil {
				return fmt.Errorf("line %d: %w", n.Line, err)
			}
		}
	}
	for i, child := range n.Content {
		nested := kind
		if n.Kind == yaml.MappingNode {
			nested = syntaxData
			if i%2 == 1 {
				key := n.Content[i-1].Value
				// The typed traversal already checked these as CEL (or the
				// child condition's supported raw/interpolated forms).
				if key == "when" && (kind == syntaxNode || kind == syntaxCondition || kind == syntaxChild) {
					continue
				}
				nested = childJobSyntax(kind, key)
			}
		}
		if err := checkJobSyntax(child, version, nested); err != nil {
			return err
		}
	}
	return nil
}

func childJobSyntax(parent jobSyntax, key string) jobSyntax {
	switch parent {
	case syntaxNodeMap: // Named definitions or named state nodes.
		return syntaxNode
	case syntaxNode:
		switch key {
		case "sequence":
			return syntaxNode
		case "parallel":
			return syntaxParallel
		case "defs":
			return syntaxNodeMap
		case "state":
			return syntaxState
		case "catch", "transitions":
			return syntaxCondition
		case "child_group":
			return syntaxChildGroup
		}
	case syntaxParallel:
		if key == "branches" {
			return syntaxNode
		}
	case syntaxState:
		switch key {
		case "initial":
			return syntaxCondition
		case "states":
			return syntaxNodeMap
		}
	case syntaxChildGroup:
		if key == "children" || key == "child" {
			return syntaxChild
		}
	}
	return syntaxData
}

// ValidateJobConditions rejects retired context in typed conditions, including
// legacy snapshots, without changing their operation or wait interpretation.
func ValidateJobConditions(r Recipe) error {
	return checkRawJobExpressions(r.RecipeImpl)
}

// Raw CEL is executable only in typed condition fields. Do not interpret raw
// strings in application maps (including keys named "when") as recipe code.
// Template interpolation in those maps is checked separately by checkJobSyntax.
func checkRawJobExpressions(value any) error {
	var children []any
	switch v := value.(type) {
	case cel.CELExpr:
		return jobsemantics.CheckExpression(v.String())
	case string: // ChildGroupChild.When supports both raw CEL and interpolation.
		return jobsemantics.CheckChildCondition(v)
	case *RecipeOp:
		children = []any{v.RecipeMetadata}
	case *RecipeSequence:
		children = []any{v.RecipeMetadata, v.SequenceData}
	case *RecipeParallel:
		children = []any{v.RecipeMetadata, v.Parallel}
	case *RecipeState:
		children = []any{v.RecipeMetadata, v.StateMachineData}
	case *RecipeChildGroup:
		children = []any{v.RecipeMetadata, v.ChildGroup}
	case RecipeMetadata:
		children = append(children, v.NodeMetadata)
		for _, def := range v.Defs {
			children = append(children, def.NodeImpl)
		}
	case NodeMetadata:
		children = append(children, v.When)
		for _, clause := range v.Catch {
			children = append(children, clause.When)
		}
	case *NodeOp:
		children = []any{v.NodeMetadata}
	case *NodeInclude:
		children = []any{v.NodeMetadata}
	case *NodeSequence:
		children = []any{v.NodeMetadata, v.SequenceData}
	case *NodeParallel:
		children = []any{v.NodeMetadata, v.Parallel}
	case *NodeState:
		children = []any{v.NodeMetadata, v.StateMachineData}
	case *NodeChildGroup:
		children = []any{v.NodeMetadata, v.ChildGroup}
	case SequenceData:
		for _, node := range v.Sequence {
			children = append(children, node.NodeImpl)
		}
	case ParallelBranches:
		if err := v.Validate(); err != nil {
			return err
		}
		for _, node := range v.Branches {
			children = append(children, node.NodeImpl)
		}
	case StateMachineData:
		if v.States != nil {
			for _, transition := range v.States.Initial {
				children = append(children, transition.When)
			}
			for _, state := range v.States.States {
				children = append(children, state.NodeImpl)
				for _, transition := range state.Transitions {
					children = append(children, transition.When)
				}
			}
		}
	case ChildGroupData:
		for _, child := range v.Children {
			children = append(children, child.When)
		}
		if v.Child != nil {
			children = append(children, v.Child.When)
		}
	}
	for _, child := range children {
		if err := checkRawJobExpressions(child); err != nil {
			return err
		}
	}
	return nil
}
