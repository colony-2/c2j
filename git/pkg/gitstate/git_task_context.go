package gitstate

import "github.com/colony-2/c2j/recipe-core/pkg/contextual"

// GlobalGitTaskContext contains machine-independent git context (serializable)
// This can be safely serialized and sent between machines
type GlobalGitTaskContext struct {
	BaseRepo         string
	BaseRef          string
	ResolvedBaseHash string
	RecipeSourceRepo string
	RecipeSourceRef  string
	PersistHash      string
	ParentHash       string
	TicketID         string
	CellName         string
	CellPath         string // Cell relative path from repo root
	GitAuthor        string
	NodePath         string
	InvokeSeq        int64
	InvokeHash       string
}

// NewGlobalGitTaskContext creates a GlobalGitTaskContext from TaskExecutionContext
func NewGlobalGitTaskContext(tec contextual.TaskExecutionContext) *GlobalGitTaskContext {
	return &GlobalGitTaskContext{
		BaseRepo:         tec.GitTask.BaseRepo,
		BaseRef:          tec.GitTask.BaseRef,
		ResolvedBaseHash: tec.GitTask.ResolvedBaseHash,
		RecipeSourceRepo: tec.RecipeSource.Repo,
		RecipeSourceRef:  tec.RecipeSource.Ref,
		GitAuthor:        tec.GitTask.GitAuthor,
		PersistHash:      tec.GitTask.PersistHash,
		ParentHash:       tec.GitTask.ParentHash,
		TicketID:         tec.Actor.TicketID,
		CellName:         tec.Workflow.CellName,
		CellPath:         tec.Workflow.CellPath,
		NodePath:         tec.Invocation.NodePath,
		InvokeSeq:        tec.Invocation.InvokeSeq,
	}
}

// GitTaskContext is used internally by workspace_controller
// Uses POINTER embedding to share the global context
type GitTaskContext struct {
	*GlobalGitTaskContext // Pointer embedding - shares data with global context
	WorktreePath          string
}

// NewGitTaskContext creates a GitTaskContext from TaskExecutionContext
func NewGitTaskContext(tec contextual.TaskExecutionContext) *GitTaskContext {
	return &GitTaskContext{
		GlobalGitTaskContext: NewGlobalGitTaskContext(tec),
		WorktreePath:         tec.Environment.WorktreePath,
	}
}

// Keep ALL existing getter methods - controller and other code depends on them
func (c *GitTaskContext) GetBaseRepo() string         { return c.GlobalGitTaskContext.BaseRepo }
func (c *GitTaskContext) GetBaseRef() string          { return c.GlobalGitTaskContext.BaseRef }
func (c *GitTaskContext) GetResolvedBaseHash() string { return c.GlobalGitTaskContext.ResolvedBaseHash }
func (c *GitTaskContext) GetRecipeSourceRepo() string { return c.GlobalGitTaskContext.RecipeSourceRepo }
func (c *GitTaskContext) GetRecipeSourceRef() string  { return c.GlobalGitTaskContext.RecipeSourceRef }
func (c *GitTaskContext) GetPersistHash() string      { return c.GlobalGitTaskContext.PersistHash }
func (c *GitTaskContext) GetParentHash() string       { return c.GlobalGitTaskContext.ParentHash }
func (c *GitTaskContext) GetWorktreePath() string     { return c.WorktreePath }
func (c *GitTaskContext) GetTicketID() string         { return c.GlobalGitTaskContext.TicketID }
func (c *GitTaskContext) GetCellName() string         { return c.GlobalGitTaskContext.CellName }
func (c *GitTaskContext) GetCellPath() string         { return c.GlobalGitTaskContext.CellPath }
func (c *GitTaskContext) GetGitAuthor() string        { return c.GlobalGitTaskContext.GitAuthor }
func (c *GitTaskContext) GetNodePath() string         { return c.GlobalGitTaskContext.NodePath }
func (c *GitTaskContext) GetInvokeSeq() int64         { return c.GlobalGitTaskContext.InvokeSeq }
func (c *GitTaskContext) GetInvokeHash() string       { return c.GlobalGitTaskContext.InvokeHash }
