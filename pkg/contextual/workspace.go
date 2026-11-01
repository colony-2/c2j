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
	// TaskBaseHash is an observation for this operation, not an accepted snapshot.
	TaskBaseHash string `json:"task_base_hash,omitempty"`
}

const WorkspaceFollowUntilChange = "follow-until-change"

func (w *WorkspaceContext) FollowsRef() bool {
	return w != nil && w.RefPolicy == WorkspaceFollowUntilChange && w.Initial != "empty"
}

// HasEmptyBase distinguishes intentionally absent history from an unresolved ref.
// Initial remains provenance after publication; a materialized base ends empty mode.
func (w *WorkspaceContext) HasEmptyBase(baseHash, parentHash string) bool {
	return w != nil && w.Initial == "empty" && baseHash == "" && parentHash == ""
}
