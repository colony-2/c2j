package recipe

import (
	"testing"

	"github.com/colony-2/c2j/pkg/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"
)

func TestStateMachine_Construct_And_YAML(t *testing.T) {
	registerTestOp()
	// State machines define initial state correctly [pkg/recipe/state.go]
	sm := &RecipeState{StateMachineData: StateMachineData{States: &StateMap{Initial: InitialState("start"), States: map[string]State{
		"start": {Node: Node{NodeImpl: &NodeOp{OpData: OpData{Op: "echo"}}}, SingleStateMetadata: SingleStateMetadata{}},
	}}}}
	name, ok := sm.States.Initial.ShortcutState()
	require.True(t, ok)
	assert.Equal(t, "start", name)

	// Complete state machines serialize to YAML [pkg/recipe/state.go]
	b, err := yamlv3.Marshal(sm)
	require.NoError(t, err)
	var back RecipeState
	require.NoError(t, yamlv3.Unmarshal(b, &back))
	backName, backOK := back.States.Initial.ShortcutState()
	require.True(t, backOK)
	assert.Equal(t, "start", backName)
}

func TestStateMachine_Initial_Unmarshal_Shapes(t *testing.T) {
	registerTestOp()

	var fromString Node
	require.NoError(t, yamlUnmarshalStrict(`state: { initial: "start", states: { start: { op: echo, inputs: {message: hi}}}}`, &fromString))
	stateFromString := fromString.NodeImpl.(*NodeState)
	name, ok := stateFromString.States.Initial.ShortcutState()
	require.True(t, ok)
	assert.Equal(t, "start", name)

	var fromObject Node
	require.NoError(t, yamlUnmarshalStrict(`
state:
  initial:
    to: start
    when: true
    payload:
      message: hello
  states:
    start: {op: echo, inputs: {message: hi}}
`, &fromObject))
	stateFromObject := fromObject.NodeImpl.(*NodeState)
	require.Len(t, stateFromObject.States.Initial, 1)
	assert.Equal(t, "start", stateFromObject.States.Initial[0].To)
	assert.Equal(t, "true", stateFromObject.States.Initial[0].When.String())
	assert.Equal(t, "hello", stateFromObject.States.Initial[0].Payload["message"])

	var fromList Node
	require.NoError(t, yamlUnmarshalStrict(`
state:
  initial:
    - to: missing
      when: false
    - to: start
      when: true
  states:
    start: {op: echo, inputs: {message: hi}}
    missing: {op: echo, inputs: {message: hi}}
`, &fromList))
	stateFromList := fromList.NodeImpl.(*NodeState)
	require.Len(t, stateFromList.States.Initial, 2)
	assert.Equal(t, "missing", stateFromList.States.Initial[0].To)
	assert.Equal(t, "start", stateFromList.States.Initial[1].To)
}

func TestState_Transitions_With_CEL(t *testing.T) {
	// Conditional transitions evaluate CEL expressions [pkg/recipe/state.go]
	expr, err := cel.NewCELExpr("inputs.ok == true")
	require.NoError(t, err)
	st := State{SingleStateMetadata: SingleStateMetadata{Transitions: []Transition{{To: "next", When: *expr}}}}
	assert.Equal(t, "next", st.Transitions[0].To)
}

func TestState_UnmarshalYAML_DecodesTransitions(t *testing.T) {
	registerTestOp()
	var st State
	err := yamlUnmarshalStrict(`
op: echo
inputs:
  message: hi
transitions:
  - to: done
    when: "true"
    payload:
      reason: approved
`, &st)
	require.NoError(t, err)
	require.Len(t, st.Transitions, 1)
	assert.Equal(t, "done", st.Transitions[0].To)
	assert.Equal(t, "true", st.Transitions[0].When.String())
	assert.Equal(t, "approved", st.Transitions[0].Payload["reason"])
}
