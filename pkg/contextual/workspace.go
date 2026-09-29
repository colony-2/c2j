package contextual

// WorkspaceContext identifies a logical workspace; paths remain task-local.
type WorkspaceContext struct {
	Cell          string `json:"cell,omitempty"`
	ScopeID       string `json:"scope_id,omitempty"`
	ParentScopeID string `json:"parent_scope_id,omitempty"`
	InitialHash   string `json:"initial_hash,omitempty"`
}
