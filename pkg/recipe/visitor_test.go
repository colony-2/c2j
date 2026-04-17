package recipe

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBaseVisitor tests the default pass-through behavior
func TestBaseVisitor(t *testing.T) {
	// Create a simple recipe with nested nodes
	recipe := Recipe{
		RecipeImpl: &RecipeSequence{
			RecipeMetadata: RecipeMetadata{
				Version: "1.0",
				NodeMetadata: NodeMetadata{
					ID:     "test-recipe",
					Desc:   "Test recipe",
					Inputs: map[string]interface{}{"message": "hello"},
				},
			},
			SequenceData: SequenceData{
				Sequence: []Node{
					{NodeImpl: &NodeOp{
						NodeMetadata: NodeMetadata{ID: "op1", Inputs: map[string]interface{}{"input1": "value1"}},
						OpData:       OpData{Op: "echo"},
					}},
					{NodeImpl: &NodeShared{
						Shared: "shared1",
					}},
				},
				Outputs: map[string]interface{}{"output1": "result1"},
			},
		},
	}

	// Use base visitor (should return unchanged)
	visitor := &BaseVisitor{}
	walker := NewNodeWalker(visitor)
	result, err := walker.Walk(recipe)

	require.NoError(t, err)
	assert.Equal(t, recipe, result)
}

// TestSharedNodeResolver tests resolving shared node references
func TestSharedNodeResolver(t *testing.T) {
	// Define shared nodes
	defs := map[string]Node{
		"shared1": {NodeImpl: &NodeOp{
			NodeMetadata: NodeMetadata{ID: "shared-op", Inputs: map[string]interface{}{"message": "from shared"}},
			OpData:       OpData{Op: "echo"},
		}},
		"shared2": {NodeImpl: &NodeSequence{
			NodeMetadata: NodeMetadata{ID: "shared-seq"},
			SequenceData: SequenceData{
				Sequence: []Node{
					{NodeImpl: &NodeOp{
						NodeMetadata: NodeMetadata{ID: "nested-op", Inputs: map[string]interface{}{"message": "nested"}},
						OpData:       OpData{Op: "echo"},
					}},
				},
			},
		}},
	}

	// Create recipe with shared node references
	recipe := Recipe{
		RecipeImpl: &RecipeSequence{
			RecipeMetadata: RecipeMetadata{
				Version: "1.0",
				Defs:    defs,
			},
			SequenceData: SequenceData{
				Sequence: []Node{
					{NodeImpl: &NodeShared{Shared: "shared1"}},
					{NodeImpl: &NodeShared{Shared: "shared2"}},
				},
			},
		},
	}

	// Resolve shared nodes
	resolver := NewSharedNodeResolver(defs)
	walker := NewNodeWalker(resolver)
	result, err := walker.Walk(recipe)

	require.NoError(t, err)

	// Check that shared nodes were replaced
	resultSeq := result.RecipeImpl.(*RecipeSequence)
	assert.Len(t, resultSeq.Sequence, 2)

	// First should be the resolved op
	firstNode := resultSeq.Sequence[0].NodeImpl.(*NodeOp)
	assert.Equal(t, "shared-op", firstNode.ID)
	assert.Equal(t, "echo", firstNode.Op)

	// Second should be the resolved sequence
	secondNode := resultSeq.Sequence[1].NodeImpl.(*NodeSequence)
	assert.Equal(t, "shared-seq", secondNode.ID)
	assert.Len(t, secondNode.Sequence, 1)
}

// TestSharedNodeResolverCircularReference tests circular reference detection
func TestSharedNodeResolverCircularReference(t *testing.T) {
	// Create circular reference through nested shared nodes
	defs := map[string]Node{
		"shared1": {NodeImpl: &NodeSequence{
			SequenceData: SequenceData{
				Sequence: []Node{
					{NodeImpl: &NodeShared{Shared: "shared2"}},
				},
			},
		}},
		"shared2": {NodeImpl: &NodeSequence{
			SequenceData: SequenceData{
				Sequence: []Node{
					{NodeImpl: &NodeShared{Shared: "shared1"}}, // Circular!
				},
			},
		}},
	}

	recipe := Recipe{
		RecipeImpl: &RecipeSequence{
			RecipeMetadata: RecipeMetadata{Defs: defs},
			SequenceData: SequenceData{
				Sequence: []Node{
					{NodeImpl: &NodeShared{Shared: "shared1"}},
				},
			},
		},
	}

	resolver := NewSharedNodeResolver(defs)
	walker := NewNodeWalker(resolver)
	_, err := walker.Walk(recipe)

	require.Error(t, err, "Expected circular reference error")
	assert.Contains(t, err.Error(), "circular reference")
}

// TestSharedNodeResolverMissingDef tests error handling for missing definitions
func TestSharedNodeResolverMissingDef(t *testing.T) {
	defs := map[string]Node{
		"shared1": {NodeImpl: &NodeOp{OpData: OpData{Op: "echo"}}},
	}

	recipe := Recipe{
		RecipeImpl: &RecipeSequence{
			SequenceData: SequenceData{
				Sequence: []Node{
					{NodeImpl: &NodeShared{Shared: "missing"}},
				},
			},
		},
	}

	resolver := NewSharedNodeResolver(defs)
	walker := NewNodeWalker(resolver)
	_, err := walker.Walk(recipe)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "shared node 'missing' not found")
}

// TestNodeStateTraversal tests traversing state nodes
func TestNodeStateTraversal(t *testing.T) {
	recipe := Recipe{
		RecipeImpl: &RecipeState{
			RecipeMetadata: RecipeMetadata{Version: "1.0"},
			StateMachineData: StateMachineData{
				States: &StateMap{
					Initial: InitialState("state1"),
					States: map[string]State{
						"state1": {
							Node: Node{NodeImpl: &NodeOp{
								NodeMetadata: NodeMetadata{ID: "op1"},
								OpData:       OpData{Op: "echo"},
							}},
							SingleStateMetadata: SingleStateMetadata{
								Transitions: []Transition{
									{To: "state2"},
								},
							},
						},
						"state2": {
							Node: Node{NodeImpl: &NodeShared{Shared: "shared1"}},
						},
					},
				},
			},
		},
	}

	// Use resolver to replace shared nodes in states
	defs := map[string]Node{
		"shared1": {NodeImpl: &NodeOp{
			NodeMetadata: NodeMetadata{ID: "resolved-op"},
			OpData:       OpData{Op: "resolved"},
		}},
	}

	resolver := NewSharedNodeResolver(defs)
	walker := NewNodeWalker(resolver)
	result, err := walker.Walk(recipe)

	require.NoError(t, err)

	resultState := result.RecipeImpl.(*RecipeState)
	name, ok := resultState.States.Initial.ShortcutState()
	require.True(t, ok)
	assert.Equal(t, "state1", name)

	// Check state1 is unchanged
	state1 := resultState.States.States["state1"]
	assert.Equal(t, "op1", state1.Node.NodeImpl.(*NodeOp).ID)

	// Check state2 has resolved shared node
	state2 := resultState.States.States["state2"]
	assert.Equal(t, "resolved-op", state2.Node.NodeImpl.(*NodeOp).ID)
}

// NodeCounter is a custom visitor that counts nodes and collects IDs
type NodeCounter struct {
	BaseVisitor
	opCount   int
	nodeCount int
	ids       []string
}

func (n *NodeCounter) VisitNodeOp(node *NodeOp, path []string) (Node, error) {
	n.opCount++
	n.nodeCount++
	if node.ID != "" {
		n.ids = append(n.ids, node.ID)
	}
	return n.BaseVisitor.VisitNodeOp(node, path)
}

func (n *NodeCounter) VisitNodeSequence(node *NodeSequence, path []string) (Node, error) {
	n.nodeCount++
	if node.ID != "" {
		n.ids = append(n.ids, node.ID)
	}
	return n.BaseVisitor.VisitNodeSequence(node, path)
}

// TestCustomVisitor tests a custom visitor implementation
func TestCustomVisitor(t *testing.T) {
	counter := &NodeCounter{}

	recipe := Recipe{
		RecipeImpl: &RecipeSequence{
			RecipeMetadata: RecipeMetadata{
				NodeMetadata: NodeMetadata{ID: "main"},
			},
			SequenceData: SequenceData{
				Sequence: []Node{
					{NodeImpl: &NodeOp{
						NodeMetadata: NodeMetadata{ID: "op1"},
						OpData:       OpData{Op: "echo"},
					}},
					{NodeImpl: &NodeSequence{
						NodeMetadata: NodeMetadata{ID: "nested-seq"},
						SequenceData: SequenceData{
							Sequence: []Node{
								{NodeImpl: &NodeOp{
									NodeMetadata: NodeMetadata{ID: "op2"},
									OpData:       OpData{Op: "echo"},
								}},
							},
						},
					}},
				},
			},
		},
	}

	walker := NewNodeWalker(counter)
	_, err := walker.Walk(recipe)

	require.NoError(t, err)
	assert.Equal(t, 2, counter.opCount)
	assert.Equal(t, 3, counter.nodeCount) // 2 ops + 1 nested sequence
	assert.Contains(t, counter.ids, "op1")
	assert.Contains(t, counter.ids, "op2")
	assert.Contains(t, counter.ids, "nested-seq")
}

// SelectiveVisitor is a custom visitor that stops traversal at certain paths
type SelectiveVisitor struct {
	BaseVisitor
	visitedPaths []string
}

func (s *SelectiveVisitor) ShouldTraverseSequenceChildren(node *NodeSequence, path []string) bool {
	// Don't traverse sequences with ID "skip-me"
	return node.ID != "skip-me"
}

func (s *SelectiveVisitor) VisitNodeOp(node *NodeOp, path []string) (Node, error) {
	s.visitedPaths = append(s.visitedPaths, node.ID)
	return s.BaseVisitor.VisitNodeOp(node, path)
}

// Override VisitNodeSequence to use our custom ShouldTraverseSequenceChildren
func (s *SelectiveVisitor) VisitNodeSequence(node *NodeSequence, path []string) (Node, error) {
	if s.ShouldTraverseSequenceChildren(node, path) {
		newSequence := &NodeSequence{
			NodeMetadata: node.NodeMetadata,
			SequenceData: SequenceData{
				Outputs: node.Outputs,
			},
		}
		// Walk each child node
		for i, child := range node.Sequence {
			childPath := append(path, fmt.Sprintf("[%d]", i))
			newChild, err := s.BaseVisitor.walker.WalkNode(child, childPath)
			if err != nil {
				return Node{}, err
			}
			newSequence.Sequence = append(newSequence.Sequence, newChild)
		}
		return Node{NodeImpl: newSequence}, nil
	}
	return Node{NodeImpl: node}, nil
}

// TestShouldTraverseControl tests the traversal control methods
func TestShouldTraverseControl(t *testing.T) {
	visitor := &SelectiveVisitor{
		visitedPaths: []string{},
	}

	recipe := Recipe{
		RecipeImpl: &RecipeSequence{
			SequenceData: SequenceData{
				Sequence: []Node{
					{NodeImpl: &NodeOp{NodeMetadata: NodeMetadata{ID: "op1"}}},
					{NodeImpl: &NodeSequence{
						NodeMetadata: NodeMetadata{ID: "skip-me"},
						SequenceData: SequenceData{
							Sequence: []Node{
								{NodeImpl: &NodeOp{NodeMetadata: NodeMetadata{ID: "should-not-visit"}}},
							},
						},
					}},
					{NodeImpl: &NodeOp{NodeMetadata: NodeMetadata{ID: "op2"}}},
				},
			},
		},
	}

	walker := NewNodeWalker(visitor)
	_, err := walker.Walk(recipe)

	require.NoError(t, err)
	assert.Contains(t, visitor.visitedPaths, "op1")
	assert.Contains(t, visitor.visitedPaths, "op2")
	assert.NotContains(t, visitor.visitedPaths, "should-not-visit")
}

// TestComplexNestedStructure tests deeply nested structures
func TestComplexNestedStructure(t *testing.T) {
	defs := map[string]Node{
		"shared-seq": {NodeImpl: &NodeSequence{
			NodeMetadata: NodeMetadata{ID: "shared-sequence"},
			SequenceData: SequenceData{
				Sequence: []Node{
					{NodeImpl: &NodeOp{
						NodeMetadata: NodeMetadata{ID: "shared-op1"},
						OpData:       OpData{Op: "echo"},
					}},
				},
			},
		}},
	}

	recipe := Recipe{
		RecipeImpl: &RecipeState{
			RecipeMetadata: RecipeMetadata{
				Version: "1.0",
				Defs:    defs,
			},
			StateMachineData: StateMachineData{
				States: &StateMap{
					Initial: InitialState("state1"),
					States: map[string]State{
						"state1": {
							Node: Node{NodeImpl: &NodeSequence{
								SequenceData: SequenceData{
									Sequence: []Node{
										{NodeImpl: &NodeShared{Shared: "shared-seq"}},
										{NodeImpl: &NodeState{
											StateMachineData: StateMachineData{
												States: &StateMap{
													Initial: InitialState("nested1"),
													States: map[string]State{
														"nested1": {
															Node: Node{NodeImpl: &NodeOp{
																NodeMetadata: NodeMetadata{ID: "deeply-nested-op"},
															}},
														},
													},
												},
											},
										}},
									},
								},
							}},
						},
					},
				},
			},
		},
	}

	resolver := NewSharedNodeResolver(defs)
	walker := NewNodeWalker(resolver)
	result, err := walker.Walk(recipe)

	require.NoError(t, err)

	// Verify the structure was properly traversed and resolved
	resultState := result.RecipeImpl.(*RecipeState)
	state1 := resultState.States.States["state1"]
	seq := state1.Node.NodeImpl.(*NodeSequence)

	// First node should be resolved shared sequence
	firstNode := seq.Sequence[0].NodeImpl.(*NodeSequence)
	assert.Equal(t, "shared-sequence", firstNode.ID)
	assert.Len(t, firstNode.Sequence, 1)

	// Second node should be the nested state
	secondNode := seq.Sequence[1].NodeImpl.(*NodeState)
	nestedName, nestedOk := secondNode.States.Initial.ShortcutState()
	require.True(t, nestedOk)
	assert.Equal(t, "nested1", nestedName)
}
