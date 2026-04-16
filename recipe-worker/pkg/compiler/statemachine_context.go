package compiler

import "time"

// StateContext maintains the runtime context for state machine execution
type StateContext struct {
	CurrentState  string                            `json:"current_state"`
	Inputs        map[string]interface{}            `json:"inputs"`
	StateOutputs  map[string]map[string]interface{} `json:"state_outputs"`
	Attempts      map[string]int                    `json:"attempts"`
	StateInfo     map[string]*StateInfo             `json:"state_info"`
	RecipeContext *RecipeContext                    `json:"recipe_context,omitempty"`
	StepOutputs   map[string]interface{}            `json:"step_outputs"` // Track step outputs within current state
}

// StateInfo tracks metadata for each state
type StateInfo struct {
	Name      string    `json:"name"`
	Attempts  int       `json:"attempts"`
	EnteredAt time.Time `json:"entered_at"`
}

// RecipeContext contains system-provided context variables
type RecipeContext struct {
	Recipe      RecipeInfo      `json:"recipe"`
	Environment EnvironmentInfo `json:"environment"`
	Execution   ExecutionInfo   `json:"execution"`
}

// RecipeInfo contains information about the current recipe
type RecipeInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	ExecutionID string `json:"execution_id"`
}

// EnvironmentInfo contains environment context
type EnvironmentInfo struct {
	Name   string `json:"name"`
	Region string `json:"region"`
}

// ExecutionInfo contains execution context
type ExecutionInfo struct {
	StartedAt time.Time     `json:"started_at"`
	Timeout   time.Duration `json:"timeout"`
}

// CELVariables represents the variables available in CEL expressions
type CELVariables struct {
	Outputs map[string]interface{}            `json:"Outputs"`
	State   *StateInfo                        `json:"State"`
	States  map[string]map[string]interface{} `json:"States"`
	Inputs  map[string]interface{}            `json:"ContainerInputs"`
	Context *RecipeContext                    `json:"Context"`
	Steps   map[string]interface{}            `json:"Steps"` // Access to step outputs within current state
}
