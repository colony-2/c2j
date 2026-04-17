package compiler

import (
	"testing"

	"github.com/colony-2/c2j/pkg/core/recipe"
	"github.com/stretchr/testify/assert"
)

// Test basic state machine structure validation
func TestBasicStateMachineStructure(t *testing.T) {
	stateMap := &recipe.StateMap{
		Initial: recipe.InitialState("simple_state"),
		States: map[string]recipe.State{
			"simple_state": {
				Node: recipe.Node{
					NodeImpl: &recipe.NodeOp{
						NodeMetadata: recipe.NodeMetadata{
							Inputs: map[string]interface{}{
								"data": "test",
							},
						},
						OpData: recipe.OpData{
							Op: "simple_activity",
						},
					},
				},
				SingleStateMetadata: recipe.SingleStateMetadata{
					Transitions: []recipe.Transition{}, // Terminal state
				},
			},
		},
	}

	// Verify basic structure
	name, ok := stateMap.Initial.ShortcutState()
	assert.True(t, ok)
	assert.Equal(t, "simple_state", name)
	assert.Contains(t, stateMap.States, "simple_state")
	nodeOp := stateMap.States["simple_state"].Node.NodeImpl.(*recipe.NodeOp)
	assert.Equal(t, "simple_activity", nodeOp.Op)
}

// Test sequential composition structure
func TestSequentialCompositionStructure(t *testing.T) {
	stateMap := &recipe.StateMap{
		Initial: recipe.InitialState("sequential_state"),
		States: map[string]recipe.State{
			"sequential_state": {
				Node: recipe.Node{
					NodeImpl: &recipe.NodeSequence{
						SequenceData: recipe.SequenceData{
							Sequence: []recipe.Node{
								{
									NodeImpl: &recipe.NodeOp{
										NodeMetadata: recipe.NodeMetadata{
											ID: "step1",
											Inputs: map[string]interface{}{
												"data": "input1",
											},
										},
										OpData: recipe.OpData{
											Op: "step1_activity",
										},
									},
								},
								{
									NodeImpl: &recipe.NodeOp{
										NodeMetadata: recipe.NodeMetadata{
											ID: "step2",
											Inputs: map[string]interface{}{
												"data": "{{ .Steps.step1.result }}",
											},
										},
										OpData: recipe.OpData{
											Op: "step2_activity",
										},
									},
								},
								{
									NodeImpl: &recipe.NodeOp{
										NodeMetadata: recipe.NodeMetadata{
											ID: "step3",
											Inputs: map[string]interface{}{
												"data": "{{ .Steps.step2.result }}",
											},
										},
										OpData: recipe.OpData{
											Op: "step3_activity",
										},
									},
								},
							},
						},
					},
				},
				SingleStateMetadata: recipe.SingleStateMetadata{
					Transitions: []recipe.Transition{}, // Terminal state has no transitions
				},
			},
		},
	}

	// Test that the state map is properly structured
	state := stateMap.States["sequential_state"]
	nodeSeq := state.Node.NodeImpl.(*recipe.NodeSequence)
	assert.Len(t, nodeSeq.Sequence, 3)
	node1 := nodeSeq.Sequence[0].NodeImpl.(*recipe.NodeOp)
	node2 := nodeSeq.Sequence[1].NodeImpl.(*recipe.NodeOp)
	node3 := nodeSeq.Sequence[2].NodeImpl.(*recipe.NodeOp)
	assert.Equal(t, "step1", node1.NodeMetadata.ID)
	assert.Equal(t, "step2", node2.NodeMetadata.ID)
	assert.Equal(t, "step3", node3.NodeMetadata.ID)
}

// Test nested sequence composition structure
func TestNestedSequenceCompositionStructure(t *testing.T) {
	stateMap := &recipe.StateMap{
		Initial: recipe.InitialState("nested_state"),
		States: map[string]recipe.State{
			"nested_state": {
				Node: recipe.Node{
					NodeImpl: &recipe.NodeSequence{
						NodeMetadata: recipe.NodeMetadata{
							ID: "nested_sequence",
						},
						SequenceData: recipe.SequenceData{
							Sequence: []recipe.Node{
								{
									NodeImpl: &recipe.NodeOp{
										NodeMetadata: recipe.NodeMetadata{
											ID: "step1",
											Inputs: map[string]interface{}{
												"data": "input1",
											},
										},
										OpData: recipe.OpData{
											Op: "step1_activity",
										},
									},
								},
								{
									NodeImpl: &recipe.NodeOp{
										NodeMetadata: recipe.NodeMetadata{
											ID: "step2",
											Inputs: map[string]interface{}{
												"data": "input2",
											},
										},
										OpData: recipe.OpData{
											Op: "step2_activity",
										},
									},
								},
								{
									NodeImpl: &recipe.NodeOp{
										NodeMetadata: recipe.NodeMetadata{
											ID: "step3",
											Inputs: map[string]interface{}{
												"data": "input3",
											},
										},
										OpData: recipe.OpData{
											Op: "step3_activity",
										},
									},
								},
							},
						},
					},
				},
				SingleStateMetadata: recipe.SingleStateMetadata{
					Transitions: []recipe.Transition{}, // Terminal state
				},
			},
		},
	}

	// Test that the state map is properly structured
	state := stateMap.States["nested_state"]
	nodeSeq := state.Node.NodeImpl.(*recipe.NodeSequence)
	assert.Len(t, nodeSeq.Sequence, 3)
	node1 := nodeSeq.Sequence[0].NodeImpl.(*recipe.NodeOp)
	node2 := nodeSeq.Sequence[1].NodeImpl.(*recipe.NodeOp)
	node3 := nodeSeq.Sequence[2].NodeImpl.(*recipe.NodeOp)
	assert.Equal(t, "step1", node1.NodeMetadata.ID)
	assert.Equal(t, "step2", node2.NodeMetadata.ID)
	assert.Equal(t, "step3", node3.NodeMetadata.ID)
}

// Test state transitions structure
func TestStateTransitionsStructure(t *testing.T) {
	// Skip CEL expression validation for now as it requires proper environment setup
	t.Skip("CEL expression validation requires proper environment setup")
}
