package ops

import (
	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// GitExecutionContext exposes the current execution workspace and git metadata
// to ops without requiring user-facing inputs to mirror internal runtime state.
type GitExecutionContext struct {
	Workspace        *contextual.WorkspaceContext `json:"workspace,omitempty"`
	CellResolution   *cellref.Context             `json:"cell_resolution,omitempty"`
	RestoreArtifact  *jobdb.ArtifactKey           `json:"restore_artifact,omitempty"`
	BaseRepo         string                       `json:"base_repo,omitempty"`
	BaseRef          string                       `json:"base_ref,omitempty"`
	ResolvedBaseHash string                       `json:"resolved_base_hash,omitempty"`
	RecipeSourceRepo string                       `json:"recipe_source_repo,omitempty"`
	RecipeSourceRef  string                       `json:"recipe_source_ref,omitempty"`
	PersistHash      string                       `json:"persist_hash,omitempty"`
	ParentHash       string                       `json:"parent_hash,omitempty"`
	CellName         string                       `json:"cell_name,omitempty"`
	GitAuthor        string                       `json:"git_author,omitempty"`
	NodePath         string                       `json:"node_path,omitempty"`
	InvokeSeq        int64                        `json:"invoke_seq,omitempty"`
	InvokeHash       string                       `json:"invoke_hash,omitempty"`
	WorktreePath     string                       `json:"worktree_path,omitempty"`
}
