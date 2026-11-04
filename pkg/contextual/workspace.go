package contextual

// WorkspaceContext identifies a logical workspace; paths remain task-local.
type WorkspaceContext struct {
	Cell          string `json:"cell,omitempty"`
	ScopeID       string `json:"scope_id,omitempty"`
	ParentScopeID string `json:"parent_scope_id,omitempty"`
	Initial       string `json:"initial,omitempty"`
	InitialHash   string `json:"initial_hash,omitempty"`
	// RefPolicy is recorded at entry. An absent policy pins the snapshot.
	RefPolicy string `json:"ref_policy,omitempty"`
}

// WorkspaceFollowPerTask resolves unpinned refs in each task worker.
const WorkspaceFollowPerTask = "follow-per-task-until-change"

func (w *WorkspaceContext) FollowsRef() bool {
	return w != nil && w.RefPolicy == WorkspaceFollowPerTask && w.Initial != "empty"
}

// HasEmptyBase distinguishes intentionally absent history from an unresolved ref.
// Initial remains provenance after publication; a materialized base ends empty mode.
func (w *WorkspaceContext) HasEmptyBase(baseHash, parentHash string) bool {
	return w != nil && w.Initial == "empty" && baseHash == "" && parentHash == ""
}
