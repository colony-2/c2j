package recipe

import (
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

func LoadRecipeFromString(data []byte) (*Recipe, error) {
	return loadRecipeFromString(data, false)
}

func LoadInternalRecipeFromString(data []byte) (*Recipe, error) {
	return loadRecipeFromString(data, true)
}

func loadRecipeFromString(data []byte, allowInternalMetadata bool) (*Recipe, error) {
	recipe := &Recipe{}
	return resolve(recipe, yaml.Unmarshal(data, recipe), allowInternalMetadata)
}

func LoadRecipeFromReader(r io.Reader) (*Recipe, error) {
	return loadRecipeFromReader(r, false)
}

func LoadInternalRecipeFromReader(r io.Reader) (*Recipe, error) {
	return loadRecipeFromReader(r, true)
}

func loadRecipeFromReader(r io.Reader, allowInternalMetadata bool) (*Recipe, error) {
	recipe := &Recipe{}
	d := yaml.NewDecoder(r)
	d.KnownFields(true)
	return resolve(recipe, d.Decode(&recipe), allowInternalMetadata)
}

func resolve(recipe *Recipe, err error, allowInternalMetadata bool) (*Recipe, error) {
	if err != nil {
		// Return decode errors as-is to preserve exact validation messages
		return nil, err
	}
	resolver := NewSharedNodeResolver(recipe.GetMetdata().Defs)
	walker := NewNodeWalker(resolver)
	result, err := walker.Walk(*recipe)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve shared nodes: %w", err)
	}
	if !allowInternalMetadata {
		if err := rejectInternalMetadata(result); err != nil {
			return nil, err
		}
	}

	return &result, err
}

func rejectInternalMetadata(rec Recipe) error {
	meta := rec.GetMetdata()
	if meta.NodeMetadata.Internal != nil {
		return fmt.Errorf("__c2j_internal metadata is reserved for compiler-generated recipe snapshots")
	}
	for name, def := range meta.Defs {
		if err := rejectInternalMetadataNode(def); err != nil {
			return fmt.Errorf("defs.%s: %w", name, err)
		}
	}
	return rejectInternalMetadataRecipeImpl(rec.RecipeImpl)
}

func rejectInternalMetadataRecipeImpl(impl RecipeImpl) error {
	switch node := impl.(type) {
	case *RecipeOp:
		if node.NodeMetadata.Internal != nil {
			return fmt.Errorf("__c2j_internal metadata is reserved for compiler-generated recipe snapshots")
		}
	case *RecipeChildGroup:
		if node.NodeMetadata.Internal != nil {
			return fmt.Errorf("__c2j_internal metadata is reserved for compiler-generated recipe snapshots")
		}
	case *RecipeSequence:
		if node.NodeMetadata.Internal != nil {
			return fmt.Errorf("__c2j_internal metadata is reserved for compiler-generated recipe snapshots")
		}
		for _, child := range node.Sequence {
			if err := rejectInternalMetadataNode(child); err != nil {
				return err
			}
		}
	case *RecipeState:
		if node.NodeMetadata.Internal != nil {
			return fmt.Errorf("__c2j_internal metadata is reserved for compiler-generated recipe snapshots")
		}
		if node.States != nil {
			for name, state := range node.States.States {
				if err := rejectInternalMetadataNode(state.Node); err != nil {
					return fmt.Errorf("state %s: %w", name, err)
				}
			}
		}
	}
	return nil
}

func rejectInternalMetadataNode(node Node) error {
	switch n := node.NodeImpl.(type) {
	case *NodeShared:
		return nil
	case *NodeOp:
		if n.NodeMetadata.Internal != nil {
			return fmt.Errorf("__c2j_internal metadata is reserved for compiler-generated recipe snapshots")
		}
	case *NodeChildGroup:
		if n.NodeMetadata.Internal != nil {
			return fmt.Errorf("__c2j_internal metadata is reserved for compiler-generated recipe snapshots")
		}
	case *NodeInclude:
		if n.NodeMetadata.Internal != nil {
			return fmt.Errorf("__c2j_internal metadata is reserved for compiler-generated recipe snapshots")
		}
	case *NodeSequence:
		if n.NodeMetadata.Internal != nil {
			return fmt.Errorf("__c2j_internal metadata is reserved for compiler-generated recipe snapshots")
		}
		for _, child := range n.Sequence {
			if err := rejectInternalMetadataNode(child); err != nil {
				return err
			}
		}
	case *NodeState:
		if n.NodeMetadata.Internal != nil {
			return fmt.Errorf("__c2j_internal metadata is reserved for compiler-generated recipe snapshots")
		}
		if n.States != nil {
			for name, state := range n.States.States {
				if err := rejectInternalMetadataNode(state.Node); err != nil {
					return fmt.Errorf("state %s: %w", name, err)
				}
			}
		}
	}
	return nil
}
