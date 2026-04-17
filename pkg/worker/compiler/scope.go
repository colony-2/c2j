package compiler

// ScopedContext represents an isolated execution context for nested compositions
type ScopedContext struct {
	// Parent scope (nil for root)
	Parent *ScopedContext

	// Local step outputs for this scope only
	LocalStepOutputs map[string]interface{}

	// Reference to the global state context
	StateContext *StateContext

	// Scope identifier for debugging
	ScopeID string
}

// NewScopedContext creates a new scoped context
func NewScopedContext(parent *ScopedContext, stateCtx *StateContext, scopeID string) *ScopedContext {
	return &ScopedContext{
		Parent:           parent,
		LocalStepOutputs: make(map[string]interface{}),
		StateContext:     stateCtx,
		ScopeID:          scopeID,
	}
}

// GetStepOutput retrieves a step output from the current scope only
func (sc *ScopedContext) GetStepOutput(stepID string) (interface{}, bool) {
	if output, exists := sc.LocalStepOutputs[stepID]; exists {
		return output, true
	}
	return nil, false
}

// SetStepOutput sets a step output in the current scope
func (sc *ScopedContext) SetStepOutput(stepID string, output interface{}) {
	sc.LocalStepOutputs[stepID] = output
}

// GetTemplateData prepares template data with proper scoping
func (sc *ScopedContext) GetTemplateData() map[string]interface{} {
	data := make(map[string]interface{})

	// Global inputs (available at all levels)
	data["ContainerInputs"] = sc.StateContext.Inputs

	// State outputs (available at all levels)
	data["States"] = sc.StateContext.StateOutputs

	// Steps - only from current scope
	data["Steps"] = sc.LocalStepOutputs

	// Context (available at all levels)
	if sc.StateContext.RecipeContext != nil {
		data["Context"] = map[string]interface{}{
			"recipe": map[string]interface{}{
				"name":         sc.StateContext.RecipeContext.Recipe.Name,
				"version":      sc.StateContext.RecipeContext.Recipe.Version,
				"execution_id": sc.StateContext.RecipeContext.Recipe.ExecutionID,
			},
			"environment": map[string]interface{}{
				"name":   sc.StateContext.RecipeContext.Environment.Name,
				"region": sc.StateContext.RecipeContext.Environment.Region,
			},
			"execution": map[string]interface{}{
				"started_at": sc.StateContext.RecipeContext.Execution.StartedAt,
				"timeout":    sc.StateContext.RecipeContext.Execution.Timeout,
			},
		}
	}

	// Current state outputs if available
	if current, exists := sc.StateContext.StateOutputs[sc.StateContext.CurrentState]; exists {
		data["Outputs"] = current
	}

	return data
}

// GetCELVariables prepares variables for CEL evaluation with proper scoping
func (sc *ScopedContext) GetCELVariables(currentOutputs map[string]interface{}) map[string]interface{} {
	vars := make(map[string]interface{})

	// Current outputs
	if currentOutputs != nil {
		vars["Outputs"] = currentOutputs
	} else {
		vars["Outputs"] = make(map[string]interface{})
	}

	// State information
	if sc.StateContext.StateInfo != nil && sc.StateContext.CurrentState != "" {
		if info, exists := sc.StateContext.StateInfo[sc.StateContext.CurrentState]; exists {
			vars["State"] = map[string]interface{}{
				"name":       info.Name,
				"attempts":   info.Attempts,
				"entered_at": info.EnteredAt,
			}
		}
	}

	// Previous state outputs (global)
	if sc.StateContext.StateOutputs != nil {
		vars["States"] = sc.StateContext.StateOutputs
	} else {
		vars["States"] = make(map[string]interface{})
	}

	// Current inputs (global)
	if sc.StateContext.Inputs != nil {
		vars["ContainerInputs"] = sc.StateContext.Inputs
	} else {
		vars["ContainerInputs"] = make(map[string]interface{})
	}

	// Recipe context (global)
	if sc.StateContext.RecipeContext != nil {
		vars["Context"] = map[string]interface{}{
			"recipe": map[string]interface{}{
				"name":         sc.StateContext.RecipeContext.Recipe.Name,
				"version":      sc.StateContext.RecipeContext.Recipe.Version,
				"execution_id": sc.StateContext.RecipeContext.Recipe.ExecutionID,
			},
			"environment": map[string]interface{}{
				"name":   sc.StateContext.RecipeContext.Environment.Name,
				"region": sc.StateContext.RecipeContext.Environment.Region,
			},
			"execution": map[string]interface{}{
				"started_at": sc.StateContext.RecipeContext.Execution.StartedAt,
				"timeout":    sc.StateContext.RecipeContext.Execution.Timeout,
			},
		}
	} else {
		vars["Context"] = make(map[string]interface{})
	}

	// Step outputs - only from current scope
	vars["Steps"] = sc.LocalStepOutputs

	return vars
}

// MergeOutputs merges the outputs from a nested scope back to parent
// This is used when a nested composition completes
func (sc *ScopedContext) MergeOutputs() map[string]interface{} {
	// Return all local step outputs as a single map
	return sc.LocalStepOutputs
}
