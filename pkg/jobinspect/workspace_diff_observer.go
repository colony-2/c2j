package jobinspect

import "github.com/colony-2/c2j/pkg/git/gitstate"

// A reference is producer evidence even if it is malformed or its bytes have
// expired. Such tasks must never receive the patchless observer exception.
func hasSavedPatchEvidence(a *diffAttempt) bool {
	for _, name := range []string{"diff_from_base.diff", "diff_from_parent.diff"} {
		if _, ok := a.activity.Refs[name]; ok {
			return true
		}
		for _, art := range a.task.Artifacts {
			if art.Name == name {
				return true
			}
		}
	}
	return false
}

// Observations do not erase contradictory invocation metadata. Check only
// fields that were recorded: historical parent-present observers need not have
// all the fixed evidence required by the omitted-parent exception.
func conflictingObserverMetadata(v *diffState) bool {
	g, out, w := v.a.in.Git, v.a.activity.Git, v.w
	if g.BaseRepo != "" && !sameChangesRepository(g.BaseRepo, w.Repository) ||
		g.GetWorkspaceCellName() != "" && g.GetWorkspaceCellName() != w.Cell ||
		g.ResolvedBaseHash != "" && g.ResolvedBaseHash != w.BaseCommit ||
		g.ParentHash != "" && g.ParentHash != w.BaseCommit ||
		out.ParentHash != "" && out.ParentHash != w.BaseCommit ||
		g.PersistHash != "" && g.PersistHash != w.HeadCommit ||
		out.PersistHash != "" && out.PersistHash != w.HeadCommit {
		return true
	}
	if g.Workspace != nil && (g.Workspace.ScopeID != "" && g.Workspace.ScopeID != w.ScopeID || g.Workspace.HasEmptyBase(g.ResolvedBaseHash, g.ParentHash) != w.EmptyBase) {
		return true
	}
	return v.a.activity.Scope != w.ScopeID
}

func unchangedObserverWithoutParent(v *diffState) bool {
	a := v.a
	before, after := a.activity.WorkspaceBefore, a.activity.WorkspaceAfter
	if before == nil || after == nil || !diffStateValid(*before) || !diffStateValid(*after) ||
		!sameDiffState(*before, *after) || !sameDiffState(*after, v.w) || conflictingObserverMetadata(v) {
		return false
	}
	// This exception proves a fixed, materialized comparison. Existing initially
	// empty and parent-present paths retain their separate validation.
	if !fullHash(v.w.BaseCommit) || a.in.Git.ResolvedBaseHash != v.w.BaseCommit ||
		a.in.Git.PersistHash != v.w.HeadCommit || a.activity.Git.PersistHash != v.w.HeadCommit {
		return false
	}
	key, conflict := incomingBundleEvidence(a)
	// The existing predecessor/source binding must still prove original ownership,
	// comparison identity, eligibility and exact chapter membership before delivery.
	return !conflict && key != nil && key.Validate() == nil && key.Name == gitstate.ThinPackArtifactName
}
