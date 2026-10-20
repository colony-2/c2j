package contextual

// WorkspaceContext identifies a logical workspace; paths remain task-local.
type WorkspaceContext struct {
	Cell          string `json:"cell,omitempty"`
	ScopeID       string `json:"scope_id,omitempty"`
	ParentScopeID string `json:"parent_scope_id,omitempty"`
	Initial       string `json:"initial,omitempty"`
	InitialHash   string `json:"initial_hash,omitempty"`
}

// HasEmptyBase distinguishes intentionally absent history from an unresolved ref.
// Initial remains provenance after publication; a materialized base ends empty mode.
func (w *WorkspaceContext) HasEmptyBase(baseHash, parentHash string) bool {
	return w != nil && w.Initial == "empty" && baseHash == "" && parentHash == ""
}
