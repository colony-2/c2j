package template

import (
	"crypto/sha256"
	"fmt"

	"github.com/colony-2/c2j/pkg/contextual"
)

// NextWorkspaceID uses separate counters so legacy invocation identities do not change.
func (rc *ResolutionContext) NextWorkspaceID(node string) string {
	if rc.workspaceCounters == nil {
		rc.workspaceCounters = map[string]int64{}
	}
	key := rc.TaskExecutionContext().Workflow.JobID + "/" + rc.TemplateData.Context.Invocation.NodePath + "/" + node
	if rc.TemplateData.Context.Workspace != nil {
		key = rc.TemplateData.Context.Workspace.ScopeID + "/" + key
	}
	n := rc.workspaceCounters[key]
	rc.workspaceCounters[key] = n + 1
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s#%d", key, n))))
}

// WithWorkspace keeps output containers and lexical paths, but forks Git state.
func (rc *ResolutionContext) WithWorkspace(ws contextual.WorkspaceContext, base contextual.GitBaseContext) (*ResolutionContext, error) {
	out := *rc
	out.Parent = rc
	out.workspaceCaller = rc
	out.TemplateData = rc.TemplateData
	out.TemplateData.Context = rc.TaskExecutionContext()
	if out.TemplateData.Context.RecipeSource.Repo == "" {
		out.TemplateData.Context.RecipeSource.Repo = out.TemplateData.Context.GitTask.BaseRepo
	}
	if out.TemplateData.Context.RecipeSource.Ref == "" {
		out.TemplateData.Context.RecipeSource.Ref = out.TemplateData.Context.GitTask.BaseRef
	}
	out.TemplateData.Context.Workspace = &ws
	out.workspaceBase = &base
	out.commitContext = &contextual.GitCommitContext{ParentRef: base.BaseRef}
	out.TemplateData.Vars = cloneTemplateVars(rc.TemplateData.Vars)
	out.lastArtifacts = nil
	out.lastArtifactRefs = nil
	out.lastExecution = nil
	out.syncWorkspace()
	return out.initializeCEL()
}

// PublishWorkspaceResult exports data, never the active Git state.
func (rc *ResolutionContext) PublishWorkspaceResult(from *ResolutionContext) {
	rc.lastExecution = from.lastExecution
	rc.lastArtifacts = from.lastArtifacts
	rc.lastArtifactRefs = from.lastArtifactRefs
	rc.lastJobs = from.lastJobs
}

func (rc *ResolutionContext) syncWorkspace() {
	if rc.workspaceBase == nil {
		return
	}
	g := &rc.TemplateData.Context.GitTask
	g.BaseRepo = rc.workspaceBase.BaseRepo
	g.BaseRef = rc.workspaceBase.BaseRef
	g.ResolvedBaseHash = rc.workspaceBase.ResolvedBaseHash
	g.GitAuthor = rc.workspaceBase.GitAuthor
	g.PersistHash = rc.commitContext.PersistHash
	g.ParentHash = rc.commitContext.ParentHash
	g.ParentRef = rc.commitContext.ParentRef
}

// Composite bindings are evaluated in the calling workspace, before the override.
func (rc *ResolutionContext) ResolveCompositeInputs(input map[string]interface{}) (map[string]interface{}, error) {
	caller := rc
	if rc.workspaceCaller != nil {
		caller = rc.workspaceCaller
	}
	return caller.ResolveMap(input)
}
