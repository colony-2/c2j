package recipe

import (
	"fmt"
	"strings"

	"github.com/colony-2/c2j/internal/repository"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
)

func inheritWorkspace(start workflowctl.StartJob, g ops.GitExecutionContext) (workflowctl.StartJob, error) {
	start.JobContext.CellResolution = g.CellResolution
	if g.Workspace == nil || g.Workspace.ScopeID == "" {
		return start, nil
	}
	a, err := repository.Normalize(start.JobContext.GitBase.BaseRepo)
	if err != nil {
		return start, err
	}
	b, err := repository.Normalize(g.BaseRepo)
	if err != nil {
		return start, err
	}
	if a != b {
		return start, nil
	}
	if start.JobContext.Workflow.CellName != g.Workspace.Cell {
		return start, fmt.Errorf("child cell %q disagrees with inherited workspace %q; specify a matching child Git repository", start.JobContext.Workflow.CellName, g.Workspace.Cell)
	}
	// Explicit alternate refs start independently; they do not inherit a synthetic commit.
	if start.GitRef != g.PersistHash && start.GitRef != g.BaseRef && start.GitRef != g.ResolvedBaseHash {
		return start, nil
	}
	ws := *g.Workspace
	ws.ParentScopeID = ws.ScopeID
	ws.ScopeID = "child:" + start.JobID
	start.JobContext.Workspace = &ws
	start.JobContext.CellResolution = g.CellResolution
	commit := contextual.GitCommitContext{ParentRef: g.BaseRef}
	if strings.TrimSpace(g.PersistHash) != "" && start.GitRef == g.PersistHash {
		if g.RestoreArtifact == nil {
			return start, fmt.Errorf("inherited workspace commit requires its snapshot artifact")
		}
		commit = contextual.GitCommitContext{PersistHash: g.PersistHash, ParentHash: g.ParentHash}
		start.JobContext.RestoreArtifact = g.RestoreArtifact
	}
	start.JobContext.InitialCommit = &commit
	return start, nil
}
