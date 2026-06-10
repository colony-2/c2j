package model

import (
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/swf-go/pkg/swf"
)

// GetJobRunStoryRequest requests a recipe-centric story for a workflow/job execution.
type GetJobRunStoryRequest struct {
	ProjectID string
	JobID     string
}

// JobRunStory is a recipe-centric narrative of a job run built by replaying recipe execution
// against recorded task outcomes from swf.GetJobRunResponse.
type JobRunStory struct {
	JobID              string            `json:"job_id"`
	InvocationSequence int64             `json:"invocation_sequence"`
	Recipe             JobRunStoryRecipe `json:"recipe"`
	Status             WorkflowStatus    `json:"status"`
	StartedAt          time.Time         `json:"started_at"`
	FinishedAt         *time.Time        `json:"finished_at"`
	Root               *JobRunStoryNode  `json:"root"`
}

type JobRunStoryRecipe struct {
	ID      string                  `json:"id"`
	Name    string                  `json:"name"`
	Version string                  `json:"version"`
	Source  JobRunStoryRecipeSource `json:"source"`
}

type JobRunStoryRecipeSource struct {
	Kind                  string `json:"kind"` // jobStartArtifact|jobStartRef
	ArtifactName          string `json:"artifact_name,omitempty"`
	SubmittedSelector     string `json:"submitted_selector,omitempty"`
	ResolvedSelector      string `json:"resolved_selector,omitempty"`
	ResolvedCommit        string `json:"resolved_commit,omitempty"`
	RecipeYAML            string `json:"recipe_yaml,omitempty"`
	ResolutionTaskOrdinal *int64 `json:"resolution_task_ordinal,omitempty"`
}

type JobRunStoryNodeKind string

const (
	JobRunStoryNodeKindRecipe                 JobRunStoryNodeKind = "recipe"
	JobRunStoryNodeKindRecipeSourceResolution JobRunStoryNodeKind = "recipeSourceResolution"
	JobRunStoryNodeKindSequence               JobRunStoryNodeKind = "sequence"
	JobRunStoryNodeKindOp                     JobRunStoryNodeKind = "op"
	JobRunStoryNodeKindOpStep                 JobRunStoryNodeKind = "opStep"
	JobRunStoryNodeKindContextPatch           JobRunStoryNodeKind = "contextPatch"
	JobRunStoryNodeKindStateMachine           JobRunStoryNodeKind = "stateMachine"
	JobRunStoryNodeKindState                  JobRunStoryNodeKind = "state"
	JobRunStoryNodeKindTransitionEval         JobRunStoryNodeKind = "transitionEval"
)

type JobRunStoryNodeStatus string

const (
	JobRunStoryNodeStatusPending   JobRunStoryNodeStatus = "pending"
	JobRunStoryNodeStatusRunning   JobRunStoryNodeStatus = "running"
	JobRunStoryNodeStatusSucceeded JobRunStoryNodeStatus = "succeeded"
	JobRunStoryNodeStatusFailed    JobRunStoryNodeStatus = "failed"
	JobRunStoryNodeStatusCanceled  JobRunStoryNodeStatus = "canceled"
	JobRunStoryNodeStatusSkipped   JobRunStoryNodeStatus = "skipped"
	JobRunStoryNodeStatusUnknown   JobRunStoryNodeStatus = "unknown"
)

// JobRunStoryNode is a node in the recipe execution story tree. All fields are always present
// unless declared as pointers.
//
// Convention:
//   - The node itself represents the latest *task* attempt (retry) for that logical node.
//   - Prior *task* attempts (retries) of the same logical node are stored in PriorAttempts.
//   - This is distinct from SWF "job attempts" (GetJobRunResponse.Attempts), which are surfaced
//     at the recipe root via JobAttempt/PastAttempts.
type JobRunStoryNode struct {
	ID         string                `json:"id"`
	Kind       JobRunStoryNodeKind   `json:"kind"`
	Title      string                `json:"title"`
	Status     JobRunStoryNodeStatus `json:"status"`
	StartedAt  *time.Time            `json:"started_at"`
	FinishedAt *time.Time            `json:"finished_at"`

	Path        []string                         `json:"path"`
	InvokeSeq   int64                            `json:"invoke_seq"`
	InlineStack []contextual.InlineBoundaryFrame `json:"inline_stack,omitempty"`

	// JobAttempt is the SWF "job attempt" number (GetJobRunResponse.Attempts[i].Attempt).
	// It is only set on recipe root nodes.
	JobAttempt int `json:"job_attempt,omitempty"`
	// PastAttempts are prior SWF job attempts represented as recipe root nodes.
	// It is only set on the latest recipe root node.
	PastAttempts []*JobRunStoryNode `json:"past_attempts,omitempty"`

	Attempt       int                `json:"attempt"`
	PriorAttempts []*JobRunStoryNode `json:"prior_attempts"`

	Input        any                    `json:"input"`
	Output       any                    `json:"output"`
	RenderedVars map[string]interface{} `json:"rendered_vars,omitempty"`
	ArtifactKeys []swf.ArtifactKey      `json:"artifact_keys"`
	ArtifactRefs []recipeartifacts.Ref  `json:"artifact_refs,omitempty"`

	// TaskOrdinal is the SWF chapter ordinal for this node's latest attempt (when applicable).
	// This is the primary "restart cursor" surface for the frontend.
	TaskOrdinal *int64 `json:"task_ordinal,omitempty"`
	// RestartFromOrdinal is the first attempt's ordinal for this logical node (when applicable).
	// SWF restart cannot cut into a retry chain, so this is the safe ordinal to restart from.
	RestartFromOrdinal *int64 `json:"restart_from_ordinal,omitempty"`

	Children []*JobRunStoryNode `json:"children"`

	// Kind-specific fields (all optional; only set for relevant kinds).
	RecipeID string `json:"recipe_id,omitempty"`
	// Invocation is intentionally loose; today we only emit args.
	Invocation map[string]interface{} `json:"invocation,omitempty"`

	SequenceID string `json:"sequence_id,omitempty"`

	OpID   string            `json:"op_id,omitempty"`
	OpType string            `json:"op_type,omitempty"`
	Error  *JobRunStoryError `json:"error,omitempty"`

	StepID   string `json:"step_id,omitempty"`
	StepType string `json:"step_type,omitempty"`

	StateMachineID string `json:"state_machine_id,omitempty"`

	StateID   string `json:"state_id,omitempty"`
	IsInitial *bool  `json:"is_initial,omitempty"`

	FromStateID string                         `json:"from_state_id,omitempty"`
	Evaluations []JobRunStoryTransitionEval    `json:"evaluations,omitempty"`
	Decision    *JobRunStoryTransitionDecision `json:"decision,omitempty"`
}

type JobRunStoryError struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type JobRunStoryTransitionEval struct {
	ToStateID  string  `json:"to_state_id"`
	Expression string  `json:"expression"`
	Result     bool    `json:"result"`
	Reason     *string `json:"reason,omitempty"`
}

type JobRunStoryTransitionDecision struct {
	Kind      string                 `json:"kind"` // state|fallthrough
	ToStateID *string                `json:"to_state_id,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
}
