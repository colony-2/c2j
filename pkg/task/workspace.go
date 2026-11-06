package task

// WorkspaceState records the actual checkout used by an attempt. It is an
// observation, not a request to pin a floating workspace for later tasks.
type WorkspaceState struct {
	Repository string `json:"repository"`
	Cell       string `json:"cell,omitempty"`
	ScopeID    string `json:"scope_id,omitempty"`
	BaseRef    string `json:"base_ref,omitempty"`
	BaseCommit string `json:"base_commit,omitempty"`
	HeadCommit string `json:"head_commit,omitempty"`
	EmptyBase  bool   `json:"empty_base,omitempty"`
}
