package task

// ContextPatch is a persisted, replayable message that mutates template-visible context
// before the next task execution.
//
// Semantics:
// - Job: JSON merge patch (RFC 7396-like) applied to contextual.JobContext (by JSON shape).
// - Scopes: patch step outputs in the currently-visible scoped containers (sequence/states).
//
// Notes:
//   - We keep this intentionally JSON-shaped so callers can patch individual fields without
//     requiring per-field pointer types.
//   - Null values in the patch delete keys from maps (for map-typed sections).
type ContextPatch struct {
	// Job is a JSON merge patch applied to the job context (contextual.JobContext as JSON).
	Job map[string]any `json:"job,omitempty"`

	// Scopes patches local scoped data structures (e.g. sequence.<id>.outputs).
	Scopes []ScopePatch `json:"scopes,omitempty"`
}

type ScopePatch struct {
	// Container selects which scope output map to patch.
	// Allowed: "sequence", "states".
	Container string `json:"container"`
	// ID is the step id within the container (e.g. node metadata id).
	ID string `json:"id"`

	// Outputs is a JSON merge patch applied to the step's outputs map.
	Outputs map[string]any `json:"outputs,omitempty"`
}
