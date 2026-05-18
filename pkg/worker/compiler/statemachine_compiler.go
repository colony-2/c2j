package compiler

import (
	"fmt"
	"time"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/workflow"
)

// ExecuteStateMachine runs the state machine with the new StateMap format
func (d DefaultRecipeExecutor) ExecuteStateMachine(ctx workflow.Context, parentContext *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, stateMap *recipe.StateMap, opts ...ExecutionOptions) error {
	if timeout := time.Duration(metadata.Timeout); timeout > 0 {
		ctx.JobContext = withExecutionTimeout(ctx.JobContext, timeout, fmt.Sprintf("state machine %q", template.ScopeID(metadata, "", template.ScopeStateMachine)))
	}

	// Create resolution context for the state machine
	resolvedInputs, err := parentContext.ResolveMap(metadata.Inputs)
	if err != nil {
		return fmt.Errorf("failed to resolve state machine inputs: %w", err)
	}

	resCtx, err := parentContext.NewChildContext(template.ScopeStateMachine, metadata, "", resolvedInputs)
	if err != nil {
		return fmt.Errorf("failed to create resolution context: %w", err)
	}
	if err := resCtx.ResolveVars(metadata.Vars); err != nil {
		return fmt.Errorf("failed to resolve state machine vars: %w", err)
	}
	if err := seedStateMachinePlaceholders(resCtx, stateMap); err != nil {
		return err
	}

	var observer StateObserver

	if len(opts) > 1 {
		return fmt.Errorf("too many execution options specified")
	}
	if len(opts) == 1 && opts[0].StateObserver != nil {
		observer = opts[0].StateObserver
	} else {
		observer = NoOpStateObserver{}
	}

	// Resolve initial state using the same transition evaluator used by per-state transitions.
	initialDecision, err := evaluateInitialState(observer, stateMap.Initial, resCtx)
	if err != nil {
		return err
	}
	currentState := initialDecision.To
	currentTransition := initialDecision.Transition
	if _, ok := stateMap.States[currentState]; !ok {
		return fmt.Errorf("state '%s' not found", currentState)
	}
	stateInvocationCount := make(map[string]int)

	if resCtx.Options.Mode == template.ModeValidate && resCtx.Options.ValidationMode == string(ValidateAll) {
		stateNames := sortedStateNames(stateMap.States)
		lastStateName := ""
		lastStateDef := recipe.State{}
		for _, stateName := range stateNames {
			stateDef := stateMap.States[stateName]
			observer.StateEntered(stateName)
			if err := d.runState(ctx, resCtx, stateName, stateDef, template.NewTransitionData("", stateName, nil)); err != nil {
				return fmt.Errorf("state '%s' execution failed: %w", stateName, err)
			}
			observer.StateExited(stateName)
			lastStateName = stateName
			lastStateDef = stateDef
			if _, err := evaluateTransitionsWithContext(observer, stateDef.Transitions, resCtx, stateName); err != nil {
				return fmt.Errorf("failed to evaluate state transitions: %w", err)
			}
		}

		resolvedOutputs, err := resCtx.ResolveMap(outputTemplate)
		if err != nil {
			return fmt.Errorf("failed to resolve state machine outputs: %w", err)
		}

		parentContext.AddExecutionWithArtifactData(resolvedOutputs, stateArtifacts(resCtx, lastStateName, lastStateDef), resCtx.GetLastArtifacts())
		return nil
	}

	// Execute state machine. Always run the current state at least once, even if it is terminal.
	for {
		// Get current state definition
		stateDef, exists := stateMap.States[currentState]
		if !exists {
			return fmt.Errorf("state '%s' not found", currentState)
		}

		observer.StateEntered(currentState)
		if err := d.runState(ctx, resCtx, currentState, stateDef, currentTransition); err != nil {
			// Handle retry if configured
			return fmt.Errorf("state '%s' execution failed: %w", currentState, err)
		}
		observer.StateExited(currentState)
		// Terminal states end the machine after they run.
		if isTerminalState(currentState, stateMap.States) {
			break
		}

		// Evaluate transitions using resolution context
		decision, err := evaluateTransitionsWithContext(observer, stateDef.Transitions, resCtx, currentState)
		if err != nil {
			return fmt.Errorf("failed to evaluate state transitions: %w", err)
		}
		stateInvocationCount[currentState]++
		if decision.To == "" {
			// No transition matched, state machine completes
			break
		}

		currentState = decision.To
		currentTransition = decision.Transition
	}

	// Return final outputs
	finalState, ok := stateMap.States[currentState]
	if currentState != "" && !ok {
		return fmt.Errorf("state '%s' not found", currentState)
	}

	resolvedOutputs, err := resCtx.ResolveMap(outputTemplate)
	if err != nil {
		return fmt.Errorf("failed to resolve state machine outputs: %w", err)
	}

	parentContext.AddExecutionWithArtifactData(resolvedOutputs, stateArtifacts(resCtx, currentState, finalState), resCtx.GetLastArtifacts())

	return nil
}

type transitionDecision struct {
	To         string
	Transition template.TransitionData
}

func evaluateInitialState(obs StateObserver, initial recipe.InitialTransitions, resCtx *template.ResolutionContext) (transitionDecision, error) {
	if len(initial) == 0 {
		return transitionDecision{}, fmt.Errorf("state machine initial state is required")
	}
	decision, err := evaluateTransitionsWithContext(obs, initial.Transitions(), resCtx, "")
	if err != nil {
		return transitionDecision{}, fmt.Errorf("failed to evaluate initial transitions: %w", err)
	}
	if decision.To == "" {
		return transitionDecision{}, fmt.Errorf("state machine initial transitions did not match any state")
	}
	return decision, nil
}

// isTerminalState checks if a state is terminal using the new State type
func isTerminalState(stateName string, states map[string]recipe.State) bool {
	state, exists := states[stateName]
	if !exists {
		return true // Non-existent state is terminal
	}
	return len(state.Transitions) == 0
}

// evaluateTransitionsWithContext evaluates transitions using resolution context
func evaluateTransitionsWithContext(obs StateObserver, transitions []recipe.Transition, resCtx *template.ResolutionContext, stateName string) (transitionDecision, error) {
	// Create a temporary context for transition evaluation
	evalCtx := transitionSourceContext(resCtx, stateName, "")

	for idx, transition := range transitions {
		evalCtx.TemplateData.Transition = template.NewTransitionData(stateName, transition.To, nil)
		expr := transition.When.String()
		shouldTransition, err := evalCtx.EvaluateCEL(expr)
		obs.TransitionEvalauted(transition.When.String(), shouldTransition, transition.To)
		if err != nil {
			return transitionDecision{}, fmt.Errorf("transition evaluation failed at transition %d from %q to %q: failed to evaluate condition: %w", idx, stateName, transition.To, err)
		}

		if shouldTransition {
			payload, err := renderTransitionPayload(resCtx, transition, stateName)
			if err != nil {
				return transitionDecision{}, fmt.Errorf("transition evaluation failed at transition %d from %q to %q: %w", idx, stateName, transition.To, err)
			}
			notifyTransitionSelected(obs, stateName, transition.To, payload)
			return transitionDecision{
				To:         transition.To,
				Transition: template.NewTransitionData(stateName, transition.To, payload),
			}, nil
		}
	}
	return transitionDecision{}, nil
}

func notifyTransitionSelected(obs StateObserver, from string, to string, payload map[string]interface{}) {
	if selected, ok := obs.(TransitionSelectionObserver); ok {
		selected.TransitionSelected(from, to, payload)
	}
}

func renderTransitionPayload(resCtx *template.ResolutionContext, transition recipe.Transition, stateName string) (map[string]interface{}, error) {
	if len(transition.Payload) == 0 {
		return map[string]interface{}{}, nil
	}
	payloadCtx := transitionSourceContext(resCtx, stateName, transition.To)
	payload, err := payloadCtx.ResolveMap(transition.Payload)
	if err != nil {
		return nil, fmt.Errorf("failed to render payload for transition to %q: %w", transition.To, err)
	}
	return payload, nil
}

func transitionSourceContext(resCtx *template.ResolutionContext, stateName string, to string) *template.ResolutionContext {
	evalCtx := &template.ResolutionContext{
		ScopeType:    resCtx.ScopeType,
		Options:      resCtx.Options,
		TemplateData: resCtx.TemplateData,
		CELEnv:       resCtx.CELEnv,
	}
	evalCtx.TemplateData.Outputs = sourceStateOutputs(resCtx, stateName)
	evalCtx.TemplateData.Transition = template.NewTransitionData(stateName, to, nil)
	return evalCtx
}

func sourceStateOutputs(resCtx *template.ResolutionContext, stateName string) map[string]interface{} {
	if stateName == "" {
		return map[string]interface{}{}
	}
	state, ok := resCtx.TemplateData.States[stateName]
	if !ok || state.Outputs == nil {
		return map[string]interface{}{}
	}
	return state.Outputs
}

func (d DefaultRecipeExecutor) runState(ctx workflow.Context, resCtx *template.ResolutionContext, stateName string, node recipe.State, transition template.TransitionData) error {
	stateResCtx, err := resCtx.NewChildContext(template.ScopeState, node.GetMetadata(), stateName, nil)
	if err != nil {
		return fmt.Errorf("failed to create state context: %w", err)
	}
	stateResCtx.TemplateData.Transition = transition.Clone()
	if err := stateResCtx.ResolveVars(node.GetMetadata().Vars); err != nil {
		return fmt.Errorf("failed to resolve state vars: %w", err)
	}

	stateNode := nodeWithoutVars(node.Node)
	return d.self().ExecuteNode(ctx, stateResCtx, &stateNode)
}

func nodeWithoutVars(node recipe.Node) recipe.Node {
	switch n := node.NodeImpl.(type) {
	case *recipe.NodeOp:
		clone := *n
		clone.NodeMetadata.Vars = nil
		return recipe.Node{NodeImpl: &clone}
	case *recipe.NodeSequence:
		clone := *n
		clone.NodeMetadata.Vars = nil
		return recipe.Node{NodeImpl: &clone}
	case *recipe.NodeState:
		clone := *n
		clone.NodeMetadata.Vars = nil
		return recipe.Node{NodeImpl: &clone}
	default:
		return node
	}
}
