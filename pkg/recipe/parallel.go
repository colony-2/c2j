package recipe

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const ParallelWaitAll = "wait_all"

func validateParallelDiscriminator(raw map[string]any) error {
	value, exists := raw["parallel"]
	if !exists {
		return nil
	}
	if value == nil {
		return fmt.Errorf("parallel must be a mapping")
	}
	for _, name := range []string{"op", "sequence", "state", "child_group", "include", "shared"} {
		if _, exists := raw[name]; exists {
			return fmt.Errorf("parallel cannot be combined with %s on the same node", name)
		}
	}
	return nil
}

type ParallelData struct {
	Parallel ParallelBranches       `yaml:"parallel"`
	Outputs  map[string]interface{} `yaml:"outputs,omitempty"`
}

type ParallelBranches struct {
	Mode     string   `yaml:"mode,omitempty" jsonschema:"enum=wait_all"`
	Branches NodeList `yaml:"branches" jsonschema:"required"`
}

func (branches *ParallelBranches) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("parallel must be a mapping")
	}
	var hasBranches bool
	for index := 0; index < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		switch key.Value {
		case "mode":
			if value.Tag != "!!str" || value.Value != ParallelWaitAll {
				return fmt.Errorf("parallel mode %q is unsupported; expected %s", value.Value, ParallelWaitAll)
			}
		case "branches":
			hasBranches = value.Kind == yaml.SequenceNode
		default:
			return fmt.Errorf("unknown parallel field %q", key.Value)
		}
	}
	if !hasBranches {
		return fmt.Errorf("parallel.branches must be a list")
	}
	type plain ParallelBranches
	return node.Decode((*plain)(branches))
}

func (branches ParallelBranches) Validate() error {
	if branches.Mode != "" && branches.Mode != ParallelWaitAll {
		return fmt.Errorf("parallel mode %q is unsupported; expected %s", branches.Mode, ParallelWaitAll)
	}
	seen := make(map[string]bool, len(branches.Branches))
	for index, branch := range branches.Branches {
		_, shared := branch.NodeImpl.(*NodeShared)
		if branch.NodeImpl == nil || shared || strings.TrimSpace(branch.GetMetadata().ID) == "" {
			return fmt.Errorf("parallel branch %d requires a nonempty id", index+1)
		}
		id := branch.GetMetadata().ID
		if seen[id] {
			return fmt.Errorf("duplicate parallel branch id %q", id)
		}
		seen[id] = true
	}
	return nil
}

type RecipeParallel struct {
	RecipeMetadata `yaml:",inline" refer:"true"`
	ParallelData   `yaml:",inline" refer:"true"`
}

func (node *RecipeParallel) GetMetadata() RecipeMetadata { return node.RecipeMetadata }
func (*RecipeParallel) isRecipe()                        {}

type NodeParallel struct {
	NodeMetadata `yaml:",inline"`
	ParallelData `yaml:",inline"`
}

func (node *NodeParallel) GetMetadata() NodeMetadata { return node.NodeMetadata }
func (*NodeParallel) isNode()                        {}
