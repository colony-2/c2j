package contextual

// WorkspaceContext identifies a logical workspace; paths remain task-local.
type WorkspaceContext struct {
	Cell          string `json:"cell,omitempty"`
	ScopeID       string `json:"scope_id,omitempty"`
	ParentScopeID string `json:"parent_scope_id,omitempty"`
	Initial       string `json:"initial,omitempty"`
	InitialHash   string `json:"initial_hash,omitempty"`
	// RefPolicy is recorded at entry. An absent policy retains legacy pinning.
	RefPolicy string `json:"ref_policy,omitempty"`
	// TaskBaseHash is a legacy operation observation, not an accepted snapshot.
	TaskBaseHash string `json:"task_base_hash,omitempty"`
}

const WorkspaceFollowUntilChange = "follow-until-change"

// WorkspaceFollowPerTask resolves unpinned refs in existing task workers.
// Keep the old policy distinct: its histories contain base-resolution tasks.
const WorkspaceFollowPerTask = "follow-per-task-until-change"

func (w *WorkspaceContext) FollowsRef() bool {
	return w != nil && (w.RefPolicy == WorkspaceFollowUntilChange || w.RefPolicy == WorkspaceFollowPerTask) && w.Initial != "empty"
}

// UsesBaseTasks preserves the task sequence of histories emitted before
// per-task resolution, including their operation retry and hook IDs.
func (w *WorkspaceContext) UsesBaseTasks() bool {
	return w.FollowsRef() && w.RefPolicy == WorkspaceFollowUntilChange
}

// HasEmptyBase distinguishes intentionally absent history from an unresolved ref.
// Initial remains provenance after publication; a materialized base ends empty mode.
func (w *WorkspaceContext) HasEmptyBase(baseHash, parentHash string) bool {
	return w != nil && w.Initial == "empty" && baseHash == "" && parentHash == ""
}
