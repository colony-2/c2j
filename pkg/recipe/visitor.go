package recipe

import (
	"fmt"
	"reflect"
)

// NodeVisitor defines the interface for visiting and transforming recipe node trees
type NodeVisitor interface {
	// Root node visitors - return (replacement, error)
	VisitRecipe(recipe *Recipe) (Recipe, error)
	VisitRecipeOp(node *RecipeOp, path []string) (*RecipeOp, error)
	VisitRecipeSequence(node *RecipeSequence, path []string) (*RecipeSequence, error)
	VisitRecipeState(node *RecipeState, path []string) (*RecipeState, error)
	VisitRecipeChildGroup(node *RecipeChildGroup, path []string) (*RecipeChildGroup, error)

	// Intermediate node visitors - return (replacement, error)
	VisitNodeOp(node *NodeOp, path []string) (Node, error)
	VisitNodeChildGroup(node *NodeChildGroup, path []string) (Node, error)
	VisitNodeInclude(node *NodeInclude, path []string) (Node, error)
	VisitNodeShared(node *NodeShared, path []string) (Node, error)
	VisitNodeSequence(node *NodeSequence, path []string) (Node, error)
	VisitNodeState(node *NodeState, path []string) (Node, error)

	// Control whether to traverse children of container nodes
	ShouldTraverseSequenceChildren(node *NodeSequence, path []string) bool
	ShouldTraverseStateChildren(node *NodeState, path []string) bool
}

// BaseVisitor provides default pass-through implementations
// Embed this to only override specific node types you care about
type BaseVisitor struct {
	walker *NodeWalker // Set by walker to enable child traversal
}

func (b *BaseVisitor) VisitRecipe(recipe *Recipe) (Recipe, error) {
	switch r := recipe.RecipeImpl.(type) {
	case *RecipeOp:
		newOp, err := b.VisitRecipeOp(r, []string{"root"})
		if err != nil {
			return Recipe{}, err
		}
		return Recipe{RecipeImpl: newOp}, nil
	case *RecipeState:
		newState, err := b.VisitRecipeState(r, []string{"root"})
		if err != nil {
			return Recipe{}, err
		}
		return Recipe{RecipeImpl: newState}, nil
	case *RecipeSequence:
		newSeq, err := b.VisitRecipeSequence(r, []string{"root"})
		if err != nil {
			return Recipe{}, err
		}
		return Recipe{RecipeImpl: newSeq}, nil
	case *RecipeChildGroup:
		newGroup, err := b.VisitRecipeChildGroup(r, []string{"root"})
		if err != nil {
			return Recipe{}, err
		}
		return Recipe{RecipeImpl: newGroup}, nil
	default:
		return Recipe{}, fmt.Errorf("unknown recipe type: %T", recipe.RecipeImpl)
	}

}

// Leaf nodes - just return unchanged
func (b *BaseVisitor) VisitNodeOp(node *NodeOp, path []string) (Node, error) {
	return Node{NodeImpl: node}, nil
}

func (b *BaseVisitor) VisitNodeChildGroup(node *NodeChildGroup, path []string) (Node, error) {
	return Node{NodeImpl: node}, nil
}

func (b *BaseVisitor) VisitNodeInclude(node *NodeInclude, path []string) (Node, error) {
	return Node{NodeImpl: node}, nil
}

func (b *BaseVisitor) VisitNodeShared(node *NodeShared, path []string) (Node, error) {
	return Node{NodeImpl: node}, nil
}

// Container nodes - must traverse children
func (b *BaseVisitor) VisitNodeSequence(node *NodeSequence, path []string) (Node, error) {
	if b.ShouldTraverseSequenceChildren(node, path) {
		newSequence := &NodeSequence{
			NodeMetadata: node.NodeMetadata,
			SequenceData: SequenceData{
				Outputs: node.Outputs,
			},
		}
		// Walk each child node
		for i, child := range node.Sequence {
			childPath := append(path, fmt.Sprintf("[%d]", i))
			newChild, err := b.walker.WalkNode(child, childPath)
			if err != nil {
				return Node{}, err
			}
			newSequence.Sequence = append(newSequence.Sequence, newChild)
		}
		return Node{NodeImpl: newSequence}, nil
	}
	return Node{NodeImpl: node}, nil
}

func (b *BaseVisitor) VisitNodeState(node *NodeState, path []string) (Node, error) {
	if b.ShouldTraverseStateChildren(node, path) {
		newState := &NodeState{
			NodeMetadata: node.NodeMetadata,
			StateMachineData: StateMachineData{
				Outputs: node.Outputs,
				States: &StateMap{
					Initial: node.States.Initial,
					States:  make(map[string]State),
				},
			},
		}
		// Walk each state's node
		for name, state := range node.States.States {
			statePath := append(path, name)
			newNode, err := b.walker.WalkNode(state.Node, statePath)
			if err != nil {
				return Node{}, err
			}
			newState.States.States[name] = State{
				Node:                newNode,
				SingleStateMetadata: state.SingleStateMetadata,
			}
		}
		return Node{NodeImpl: newState}, nil
	}
	return Node{NodeImpl: node}, nil
}

// Root recipe nodes
func (b *BaseVisitor) VisitRecipeOp(node *RecipeOp, path []string) (*RecipeOp, error) {
	// RecipeOp is a leaf node, just return it unchanged
	return node, nil
}

func (b *BaseVisitor) VisitRecipeChildGroup(node *RecipeChildGroup, path []string) (*RecipeChildGroup, error) {
	return node, nil
}

func (b *BaseVisitor) VisitRecipeSequence(node *RecipeSequence, path []string) (*RecipeSequence, error) {
	// Must traverse the embedded NodeSequence
	innerNode := NodeSequence{
		NodeMetadata: node.NodeMetadata,
		SequenceData: node.SequenceData,
	}
	result, err := b.VisitNodeSequence(&innerNode, path)
	if err != nil {
		return nil, err
	}

	if seq, ok := result.NodeImpl.(*NodeSequence); ok {
		return &RecipeSequence{
			RecipeMetadata: node.RecipeMetadata,
			SequenceData:   seq.SequenceData,
		}, nil
	}
	return node, nil
}

func (b *BaseVisitor) VisitRecipeState(node *RecipeState, path []string) (*RecipeState, error) {
	// Must traverse the embedded NodeState
	innerNode := NodeState{
		NodeMetadata:     node.NodeMetadata,
		StateMachineData: node.StateMachineData,
	}
	result, err := b.VisitNodeState(&innerNode, path)
	if err != nil {
		return nil, err
	}

	if state, ok := result.NodeImpl.(*NodeState); ok {
		return &RecipeState{
			RecipeMetadata:   node.RecipeMetadata,
			StateMachineData: state.StateMachineData,
		}, nil
	}
	return node, nil
}

// Control methods default to true (always traverse)
func (b *BaseVisitor) ShouldTraverseSequenceChildren(node *NodeSequence, path []string) bool {
	return true
}

func (b *BaseVisitor) ShouldTraverseStateChildren(node *NodeState, path []string) bool {
	return true
}

// NodeWalker orchestrates the traversal of a recipe tree
type NodeWalker struct {
	visitor NodeVisitor
}

// NewNodeWalker creates a new walker with the given visitor
func NewNodeWalker(visitor NodeVisitor) *NodeWalker {
	w := &NodeWalker{visitor: visitor}
	// If visitor is BaseVisitor or embeds it, set the walker reference
	if base, ok := visitor.(*BaseVisitor); ok {
		base.walker = w
	}
	// Handle embedded BaseVisitor
	if v := reflect.ValueOf(visitor).Elem(); v.Kind() == reflect.Struct {
		if field := v.FieldByName("BaseVisitor"); field.IsValid() {
			if base, ok := field.Addr().Interface().(*BaseVisitor); ok {
				base.walker = w
			}
		}
	}
	return w
}

// Walk traverses the entire recipe tree
func (w *NodeWalker) Walk(recipe Recipe) (Recipe, error) {
	switch r := recipe.RecipeImpl.(type) {
	case *RecipeOp:
		newOp, err := w.visitor.VisitRecipeOp(r, []string{"root"})
		if err != nil {
			return recipe, err
		}
		return Recipe{RecipeImpl: newOp}, nil

	case *RecipeSequence:
		newSeq, err := w.visitor.VisitRecipeSequence(r, []string{"root"})
		if err != nil {
			return recipe, err
		}
		return Recipe{RecipeImpl: newSeq}, nil

	case *RecipeState:
		newState, err := w.visitor.VisitRecipeState(r, []string{"root"})
		if err != nil {
			return recipe, err
		}
		return Recipe{RecipeImpl: newState}, nil

	case *RecipeChildGroup:
		newGroup, err := w.visitor.VisitRecipeChildGroup(r, []string{"root"})
		if err != nil {
			return recipe, err
		}
		return Recipe{RecipeImpl: newGroup}, nil
	}
	return recipe, fmt.Errorf("unknown recipe type: %T", recipe.RecipeImpl)
}

// WalkNode recursively processes intermediate nodes
func (w *NodeWalker) WalkNode(node Node, path []string) (Node, error) {
	switch n := node.NodeImpl.(type) {
	case *NodeOp:
		return w.visitor.VisitNodeOp(n, path)

	case *NodeShared:
		return w.visitor.VisitNodeShared(n, path)

	case *NodeChildGroup:
		return w.visitor.VisitNodeChildGroup(n, path)

	case *NodeInclude:
		return w.visitor.VisitNodeInclude(n, path)

	case *NodeSequence:
		return w.visitor.VisitNodeSequence(n, path)

	case *NodeState:
		return w.visitor.VisitNodeState(n, path)
	}

	return node, fmt.Errorf("unknown node type: %T", node.NodeImpl)
}

// SharedNodeResolver resolves SharedNode references to their definitions
type SharedNodeResolver struct {
	BaseVisitor // Embed to get default implementations
	defs        map[string]Node
	stack       []string // Track resolution stack for circular reference detection
}

// NewSharedNodeResolver creates a resolver with the given definitions
func NewSharedNodeResolver(defs map[string]Node) *SharedNodeResolver {
	return &SharedNodeResolver{
		BaseVisitor: BaseVisitor{},
		defs:        defs,
		stack:       []string{},
	}
}

// VisitNodeShared resolves shared node references
func (r *SharedNodeResolver) VisitNodeShared(node *NodeShared, path []string) (Node, error) {
	// Check for circular reference by looking if this shared node is already in our stack
	for _, name := range r.stack {
		if name == node.Shared {
			return Node{}, fmt.Errorf("circular reference detected: %s", node.Shared)
		}
	}

	if def, ok := r.defs[node.Shared]; ok {
		// Push to stack before resolving
		r.stack = append(r.stack, node.Shared)

		// If the definition itself is a shared node, recursively resolve it
		if sharedDef, ok := def.NodeImpl.(*NodeShared); ok {
			resolved, err := r.VisitNodeShared(sharedDef, path)
			// Pop from stack after recursive resolution
			if len(r.stack) > 0 {
				r.stack = r.stack[:len(r.stack)-1]
			}
			return resolved, err
		}

		// For container nodes (Sequence, State), we need to walk their children
		// to check for nested shared nodes
		resolvedDef, err := r.walkAndResolveNode(def, path)

		// Pop from stack after resolution
		if len(r.stack) > 0 {
			r.stack = r.stack[:len(r.stack)-1]
		}

		if err != nil {
			return Node{}, err
		}

		return resolvedDef, nil
	}
	return Node{}, fmt.Errorf("shared node '%s' not found", node.Shared)
}

// walkAndResolveNode recursively walks a node to resolve any nested shared nodes
func (r *SharedNodeResolver) walkAndResolveNode(node Node, path []string) (Node, error) {
	switch n := node.NodeImpl.(type) {
	case *NodeSequence:
		newSeq := &NodeSequence{
			NodeMetadata: n.NodeMetadata,
			SequenceData: SequenceData{
				Outputs: n.Outputs,
			},
		}
		for i, child := range n.Sequence {
			childPath := append(path, fmt.Sprintf("[%d]", i))
			if shared, ok := child.NodeImpl.(*NodeShared); ok {
				resolved, err := r.VisitNodeShared(shared, childPath)
				if err != nil {
					return Node{}, err
				}
				newSeq.Sequence = append(newSeq.Sequence, resolved)
			} else {
				resolvedChild, err := r.walkAndResolveNode(child, childPath)
				if err != nil {
					return Node{}, err
				}
				newSeq.Sequence = append(newSeq.Sequence, resolvedChild)
			}
		}
		return Node{NodeImpl: newSeq}, nil
	case *NodeState:
		newState := &NodeState{
			NodeMetadata: n.NodeMetadata,
			StateMachineData: StateMachineData{
				Outputs: n.Outputs,
				States: &StateMap{
					Initial: n.States.Initial,
					States:  make(map[string]State),
				},
			},
		}
		for name, state := range n.States.States {
			statePath := append(path, name)
			if shared, ok := state.Node.NodeImpl.(*NodeShared); ok {
				resolved, err := r.VisitNodeShared(shared, statePath)
				if err != nil {
					return Node{}, err
				}
				newState.States.States[name] = State{
					Node:                resolved,
					SingleStateMetadata: state.SingleStateMetadata,
				}
			} else {
				resolvedNode, err := r.walkAndResolveNode(state.Node, statePath)
				if err != nil {
					return Node{}, err
				}
				newState.States.States[name] = State{
					Node:                resolvedNode,
					SingleStateMetadata: state.SingleStateMetadata,
				}
			}
		}
		return Node{NodeImpl: newState}, nil
	default:
		// For leaf nodes, return as-is
		return node, nil
	}
}
