package ops

// GitExecutionContext exposes the current execution workspace and git metadata
// to ops without requiring user-facing inputs to mirror internal runtime state.
type GitExecutionContext struct {
	BaseRepo         string `json:"base_repo,omitempty"`
	BaseRef          string `json:"base_ref,omitempty"`
	ResolvedBaseHash string `json:"resolved_base_hash,omitempty"`
	RecipeSourceRepo string `json:"recipe_source_repo,omitempty"`
	RecipeSourceRef  string `json:"recipe_source_ref,omitempty"`
	PersistHash      string `json:"persist_hash,omitempty"`
	ParentHash       string `json:"parent_hash,omitempty"`
	CellName         string `json:"cell_name,omitempty"`
	GitAuthor        string `json:"git_author,omitempty"`
	NodePath         string `json:"node_path,omitempty"`
	InvokeSeq        int64  `json:"invoke_seq,omitempty"`
	InvokeHash       string `json:"invoke_hash,omitempty"`
	WorktreePath     string `json:"worktree_path,omitempty"`
}
