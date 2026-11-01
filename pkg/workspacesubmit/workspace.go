// Package workspacesubmit prepares immutable workspace state for CLI submissions.
package workspacesubmit

import (
	"context"
	"fmt"
	"strings"

	"github.com/colony-2/c2j/internal/repository"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/segmentio/ksuid"
)

const ArtifactName = "__c2j_submit_workspace__"
const Start = "start"
const Now = "now"

// Source is bound by the worker after restoration, before the operation runs.
// Worktree and Artifact are host-local and must never come from the RPC client.
type Source struct {
	Git             gitstate.GlobalGitTaskContext
	Worktree        string
	Artifact        jobdb.Artifact
	RestoreArtifact *jobdb.ArtifactKey
}

func (s Source) Apply(ctx context.Context, start workflowctl.StartJob, mode string, explicitTarget bool) (workflowctl.StartJob, error) {
	if mode != Start && mode != Now {
		return start, fmt.Errorf("unknown workspace mode %q", mode)
	}
	g := s.Git
	if g.BaseRepo == "" || g.BaseRef == "" {
		return start, fmt.Errorf("task-start workspace context is unavailable")
	}
	cell := g.GetWorkspaceCellName()
	if explicitTarget {
		a, err := repository.Normalize(start.JobContext.GitBase.BaseRepo)
		if err != nil {
			return start, err
		}
		b, err := repository.Normalize(g.BaseRepo)
		if err != nil {
			return start, err
		}
		if a != b || start.JobContext.Workflow.CellName != cell {
			return start, fmt.Errorf("target cell does not match the submitting workspace")
		}
	} else {
		start.JobContext.RecipeSource = contextual.RecipeSourceContext{Repo: g.BaseRepo, Ref: g.BaseRef}
	}
	for _, a := range start.Artifacts {
		if a.Name() == ArtifactName {
			return start, fmt.Errorf("artifact name %q is reserved", ArtifactName)
		}
	}
	ws := contextual.WorkspaceContext{Cell: cell}
	if g.Workspace != nil {
		ws = *g.Workspace
	}
	ws.ParentScopeID = ws.ScopeID
	if start.JobID == "" {
		start.JobID = ksuid.New().String()
	}
	ws.ScopeID = "child:" + start.JobID
	ws.RefPolicy = "" // Explicit snapshot selection is pinned, even for floating parents.
	ws.TaskBaseHash = ""
	ws.InitialHash = g.ResolvedBaseHash
	commit := contextual.GitCommitContext{PersistHash: g.PersistHash, ParentHash: g.ParentHash}
	if commit.PersistHash == "" {
		commit.ParentRef = g.ResolvedBaseHash
	}
	restore := s.RestoreArtifact
	var attached jobdb.Artifact
	if mode == Now {
		head, err := Capture(ctx, s.Worktree)
		if err != nil {
			return start, err
		}
		if head.Hash == g.PersistHash && g.PersistHash != "" {
			// Reuse the durable incoming artifact, not a new snapshot.
		} else if head.Hash == g.ResolvedBaseHash && !head.Dirty {
			commit = contextual.GitCommitContext{ParentRef: head.Hash}
			restore = nil
		} else {
			data, err := head.Bundle(ctx)
			if err != nil {
				head.Close()
				return start, err
			}
			attached = jobdb.NewArtifactFromBytes(ArtifactName, data)
			commit = contextual.GitCommitContext{PersistHash: head.Hash, ParentHash: g.ResolvedBaseHash}
			restore = nil
		}
		head.Close()
	}
	if commit.PersistHash != "" && attached == nil {
		if s.Artifact == nil {
			return start, fmt.Errorf("task-start workspace snapshot artifact is unavailable")
		}
		// Verify availability before submitting. Reuse its durable key when possible.
		data, err := s.Artifact.Bytes(ctx)
		if err != nil {
			return start, fmt.Errorf("read workspace snapshot: %w", err)
		}
		if restore == nil {
			attached = jobdb.NewArtifactFromBytes(ArtifactName, data)
		}
	}
	if commit.PersistHash == "" && commit.ParentRef == "" && !ws.HasEmptyBase(g.ResolvedBaseHash, g.ParentHash) {
		return start, fmt.Errorf("task-start workspace has no pinned commit")
	}
	start.JobContext.Workflow.CellName = cell
	start.JobContext.GitBase = contextual.GitBaseContext{BaseRepo: g.BaseRepo, BaseRef: g.BaseRef, ResolvedBaseHash: g.ResolvedBaseHash, GitAuthor: g.GitAuthor}
	start.JobContext.CellResolution = g.CellResolution
	start.JobContext.Workspace = &ws
	start.JobContext.InitialCommit = &commit
	start.JobContext.RestoreArtifact = restore
	start.JobContext.RestoreArtifactName = ""
	if attached != nil {
		start.Artifacts = append(start.Artifacts, attached)
		start.JobContext.RestoreArtifactName = ArtifactName
	}
	start.GitRef = strings.TrimSpace(commit.PersistHash)
	if start.GitRef == "" {
		start.GitRef = commit.ParentRef
	}
	if start.GitRef == "" {
		start.GitRef = g.BaseRef
	}
	return start, nil
}
