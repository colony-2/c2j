package recipe

import (
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestChildInheritsScopedSnapshotAndKeepsIndependentLineage(t *testing.T) {
	key := jobdb.ArtifactKey{JobId: "parent", TaskOrdinal: 3, Name: "snapshot", SizeBytes: 123}
	git := ops.GitExecutionContext{BaseRepo: "https://example.com/B.git", BaseRef: "main", ResolvedBaseHash: "base", PersistHash: "synthetic", ParentHash: "base", RestoreArtifact: &key, Workspace: &contextual.WorkspaceContext{Cell: "B", ScopeID: "parent-scope", InitialHash: "base"}}
	start := workflowctl.StartJob{JobID: "child", GitRef: "synthetic", JobContext: contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "B"}, GitBase: contextual.GitBaseContext{BaseRepo: git.BaseRepo, BaseRef: "main", ResolvedBaseHash: "base"}}}
	got, err := inheritWorkspace(start, git)
	require.NoError(t, err)
	require.Equal(t, "synthetic", got.JobContext.InitialCommit.PersistHash)
	require.Equal(t, &key, got.JobContext.RestoreArtifact)
	require.Equal(t, "child:child", got.JobContext.Workspace.ScopeID)
	require.Equal(t, "parent-scope", git.Workspace.ScopeID)
	start.JobContext.Workflow.CellName = "A"
	_, err = inheritWorkspace(start, git)
	require.ErrorContains(t, err, "disagrees")
	start.JobContext.Workflow.CellName = "B"
	git.RestoreArtifact = nil
	_, err = inheritWorkspace(start, git)
	require.ErrorContains(t, err, "snapshot artifact")
}
