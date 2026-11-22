package jobinspect

import (
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func observerEvidence() *diffState {
	w := coretask.WorkspaceState{Repository: "example.test/saved", Cell: "saved", ScopeID: "root", BaseCommit: strings.Repeat("a", 40), HeadCommit: strings.Repeat("b", 40)}
	before, after := w, w
	a := &diffAttempt{attempt: &attempt{}, contract: 1}
	a.activity = activity{WorkspaceBefore: &before, WorkspaceAfter: &after, Scope: w.ScopeID, Git: contextual.GitCommitContext{PersistHash: w.HeadCommit}}
	a.in.Git = gitstate.GlobalGitTaskContext{BaseRepo: w.Repository, CellName: "execution", ResolvedBaseHash: w.BaseCommit, PersistHash: w.HeadCommit, Workspace: &contextual.WorkspaceContext{Cell: w.Cell, ScopeID: w.ScopeID, Initial: "empty"}}
	a.in.Restore = &jobdb.ArtifactKey{JobId: "original", TaskOrdinal: 7, Name: gitstate.ThinPackArtifactName, SizeBytes: 10}
	return &diffState{w: w, a: a}
}

func TestWorkspaceDiffPatchlessObserverEvidence(t *testing.T) {
	cases := []struct {
		name   string
		change func(*diffState)
		valid  bool
	}{
		{"omitted-parents", func(*diffState) {}, true},
		{"input-parent-present", func(v *diffState) { v.a.in.Git.ParentHash = v.w.BaseCommit }, true},
		{"output-parent-present", func(v *diffState) { v.a.activity.Git.ParentHash = v.w.BaseCommit }, true},
		{"parents-present-legacy", func(v *diffState) {
			v.a.activity.Git.ParentHash = v.w.BaseCommit
			v.a.in.Git.ResolvedBaseHash = ""
			v.a.in.Git.ParentHash = v.w.BaseCommit
			v.a.activity.WorkspaceBefore = nil
		}, true},
		{"normalized-repository", func(v *diffState) { v.a.in.Git.BaseRepo = "https://example.test/saved.git" }, true},
		{"entry-provenance-not-active-base", func(v *diffState) { v.a.in.Git.Workspace.InitialHash = strings.Repeat("c", 40) }, true},
		{"no-before", func(v *diffState) { v.a.activity.WorkspaceBefore = nil }, false},
		{"no-after", func(v *diffState) { v.a.activity.WorkspaceAfter = nil }, false},
		{"changed", func(v *diffState) { v.a.activity.WorkspaceBefore.HeadCommit = strings.Repeat("c", 40) }, false},
		{"no-fixed-base", func(v *diffState) { v.a.in.Git.ResolvedBaseHash = "" }, false},
		{"no-fixed-head", func(v *diffState) { v.a.in.Git.PersistHash = "" }, false},
		{"no-output-head", func(v *diffState) { v.a.activity.Git.PersistHash = "" }, false},
		{"input-parent-conflict", func(v *diffState) { v.a.in.Git.ParentHash = v.w.HeadCommit }, false},
		{"output-parent-conflict", func(v *diffState) { v.a.activity.Git.ParentHash = v.w.HeadCommit }, false},
		{"resolved-base-conflict", func(v *diffState) { v.a.in.Git.ResolvedBaseHash = v.w.HeadCommit }, false},
		{"input-head-conflict", func(v *diffState) { v.a.in.Git.PersistHash = v.w.BaseCommit }, false},
		{"output-head-conflict", func(v *diffState) { v.a.activity.Git.PersistHash = v.w.BaseCommit }, false},
		{"repository-conflict", func(v *diffState) { v.a.in.Git.BaseRepo = "example.test/other" }, false},
		{"cell-conflict", func(v *diffState) { v.a.in.Git.Workspace.Cell = "other" }, false},
		{"scope-conflict", func(v *diffState) { v.a.in.Git.Workspace.ScopeID = "other" }, false},
		{"active-scope-conflict", func(v *diffState) { v.a.activity.Scope = "other" }, false},
		{"empty-conflict", func(v *diffState) {
			v.a.activity.WorkspaceBefore.EmptyBase = true
			v.a.activity.WorkspaceAfter.EmptyBase = true
		}, false},
		{"no-snapshot", func(v *diffState) { v.a.in.Restore = nil }, false},
		{"wrong-snapshot-name", func(v *diffState) { v.a.in.Restore.Name = "arbitrary" }, false},
		{"unsupported-contract", func(v *diffState) { v.a.contract = 2 }, false},
		{"patch-artifact", func(v *diffState) { v.a.task.Artifacts = []Artifact{{Name: "diff_from_base.diff"}} }, false},
		{"parent-artifact", func(v *diffState) { v.a.task.Artifacts = []Artifact{{Name: "diff_from_parent.diff"}} }, false},
		{"malformed-base-reference", func(v *diffState) { v.a.activity.Refs = map[string]artifacts.Ref{"diff_from_base.diff": {}} }, false},
		{"malformed-parent-reference", func(v *diffState) { v.a.activity.Refs = map[string]artifacts.Ref{"diff_from_parent.diff": {}} }, false},
		{"unrelated-filename", func(v *diffState) { v.a.task.Artifacts = []Artifact{{Name: "arbitrary.diff"}} }, true},
		{"parent-present-input-conflict", func(v *diffState) {
			v.a.activity.Git.ParentHash = v.w.BaseCommit
			v.a.in.Git.ParentHash = v.w.HeadCommit
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := observerEvidence()
			tc.change(v)
			art, reason := (&diffHistory{}).savedPatch(v)
			require.Nil(t, art)
			if tc.valid {
				require.Equal(t, "no saved cumulative patch for selected state", reason)
			} else {
				require.True(t, strings.HasPrefix(reason, "invalid") || strings.HasPrefix(reason, "conflicting"), reason)
			}
		})
	}
}
